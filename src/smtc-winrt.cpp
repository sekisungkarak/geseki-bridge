/*
 * Windows SMTC reader (WinRT).
 *
 * Replaces the external "SMTC Bridge" tray app: the plugin reads the media
 * sessions itself, so a streamer only runs OBS.
 *
 * Every WinRT call can throw hresult_error; the public functions swallow those
 * and degrade to "feature absent" so a machine without SMTC (or a Windows
 * build without the WinRT projection) never takes the plugin down.
 */
#include "smtc-winrt.hpp"

#include <mutex>

#ifdef _WIN32

#include <winrt/base.h>
#include <winrt/Windows.Foundation.h>
#include <winrt/Windows.Foundation.Collections.h>
#include <winrt/Windows.Media.Control.h>
#include <winrt/Windows.Storage.Streams.h>

#include <chrono>

using namespace winrt::Windows::Media::Control;
using namespace winrt::Windows::Storage::Streams;

namespace {

// WinRT needs an apartment per thread. OBS calls us from a worker thread we
// own, so initialise once, lazily, and keep it for the process lifetime.
void EnsureApartment()
{
	static std::once_flag once;
	std::call_once(once, [] {
		try {
			winrt::init_apartment(winrt::apartment_type::multi_threaded);
		} catch (...) {
			// Already initialised by the host (OBS uses COM too), fine.
		}
	});
}

int64_t TicksToMs(winrt::Windows::Foundation::TimeSpan const &ts)
{
	return std::chrono::duration_cast<std::chrono::milliseconds>(ts).count();
}

// WinRT DateTime counts 100ns ticks from 1601-01-01; subtract the offset to
// 1970-01-01 to get a Unix timestamp.
std::string ToIso8601(winrt::Windows::Foundation::DateTime const &dt)
{
	constexpr int64_t kEpochDiff = 116444736000000000LL; // 100ns units
	const int64_t ticks = dt.time_since_epoch().count() - kEpochDiff;
	const int64_t secs = ticks / 10000000LL;
	if (secs <= 0)
		return "";

	std::time_t t = static_cast<std::time_t>(secs);
	std::tm tm{};
	gmtime_s(&tm, &t);
	char buf[32];
	std::strftime(buf, sizeof(buf), "%Y-%m-%dT%H:%M:%SZ", &tm);
	return buf;
}

std::string ToUtf8(winrt::hstring const &h)
{
	return winrt::to_string(h);
}

bool ReadThumbnail(GlobalSystemMediaTransportControlsSessionMediaProperties const &media,
		   std::vector<uint8_t> &out)
{
	auto ref = media.Thumbnail();
	if (!ref)
		return false;

	auto stream = ref.OpenReadAsync().get();
	const uint64_t size = stream.Size();
	if (size == 0 || size > 32ull * 1024 * 1024)
		return false;

	DataReader reader(stream.GetInputStreamAt(0));
	reader.LoadAsync(static_cast<uint32_t>(size)).get();

	out.resize(static_cast<size_t>(size));
	reader.ReadBytes(out);
	return !out.empty();
}

} // namespace

namespace geseki::smtc {

bool Available()
{
	try {
		EnsureApartment();
		auto mgr = GlobalSystemMediaTransportControlsSessionManager::RequestAsync().get();
		return static_cast<bool>(mgr);
	} catch (...) {
		return false;
	}
}

Snapshot Poll()
{
	Snapshot snap;
	try {
		EnsureApartment();

		auto mgr = GlobalSystemMediaTransportControlsSessionManager::RequestAsync().get();
		if (!mgr)
			return snap;

		auto current = mgr.GetCurrentSession();
		if (current) {
			snap.current_session_id = ToUtf8(current.SourceAppUserModelId());
		}

		for (auto const &s : mgr.GetSessions()) {
			Session out;
			try {
				out.source_app_id = ToUtf8(s.SourceAppUserModelId());

				auto playback = s.GetPlaybackInfo();
				if (playback) {
					out.playback_status = static_cast<int>(playback.PlaybackStatus());
					// Every property below is a nullable IReference<T>. When
					// a session does not report one, the reference comes back
					// EMPTY, and calling .Value() on an empty reference is a
					// null dereference inside the WinRT projection, an access
					// violation that no catch(...) can intercept (it is SEH,
					// not C++ EH). Each one must therefore be tested before
					// its value is read; skipping that check crashed OBS.
					if (auto ptype = playback.PlaybackType())
						out.playback_type = static_cast<int>(ptype.Value());
					if (auto prate = playback.PlaybackRate())
						out.playback_rate = prate.Value();
					if (auto pshuf = playback.IsShuffleActive())
						out.is_shuffle_active = pshuf.Value();
					if (auto prepeat = playback.AutoRepeatMode())
						out.auto_repeat_mode = static_cast<int>(prepeat.Value());
				}

				auto timeline = s.GetTimelineProperties();
				if (timeline) {
					out.position_ms = TicksToMs(timeline.Position());
					out.start_ms = TicksToMs(timeline.StartTime());
					out.end_ms = TicksToMs(timeline.EndTime());
					out.min_seek_ms = TicksToMs(timeline.MinSeekTime());
					out.max_seek_ms = TicksToMs(timeline.MaxSeekTime());
					out.last_updated_time = ToIso8601(timeline.LastUpdatedTime());
				}

				auto media = s.TryGetMediaPropertiesAsync().get();
				if (media) {
					out.title = ToUtf8(media.Title());
					out.artist = ToUtf8(media.Artist());
					out.album_title = ToUtf8(media.AlbumTitle());
					out.album_artist = ToUtf8(media.AlbumArtist());
					out.subtitle = ToUtf8(media.Subtitle());
					out.track_number = media.TrackNumber();
					out.album_track_count = media.AlbumTrackCount();
					for (auto const &g : media.Genres())
						out.genres.push_back(ToUtf8(g));

					try {
						ReadThumbnail(media, out.thumbnail);
					} catch (...) {
						out.thumbnail.clear();
					}
				}
			} catch (...) {
				// A single broken session must not hide the others.
				continue;
			}
			snap.sessions.push_back(std::move(out));
		}
	} catch (...) {
		// Manager unavailable: return whatever we collected (usually nothing).
	}
	return snap;
}

bool Control(const std::string &action, int64_t position_ms)
{
	try {
		EnsureApartment();

		auto mgr = GlobalSystemMediaTransportControlsSessionManager::RequestAsync().get();
		if (!mgr)
			return false;
		auto session = mgr.GetCurrentSession();
		if (!session)
			return false;

		if (action == "play")
			return session.TryPlayAsync().get();
		if (action == "pause")
			return session.TryPauseAsync().get();
		if (action == "toggle")
			return session.TryTogglePlayPauseAsync().get();
		if (action == "next")
			return session.TrySkipNextAsync().get();
		if (action == "previous")
			return session.TrySkipPreviousAsync().get();
		if (action == "seek")
			return session.TryChangePlaybackPositionAsync(position_ms * 10000LL).get();
	} catch (...) {
		return false;
	}
	return false;
}

} // namespace geseki::smtc

#else // !_WIN32

namespace geseki::smtc {

bool Available()
{
	return false;
}

Snapshot Poll()
{
	return {};
}

bool Control(const std::string &, int64_t)
{
	return false;
}

} // namespace geseki::smtc

#endif
