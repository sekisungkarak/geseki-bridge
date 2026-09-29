#pragma once

#include <cstdint>
#include <string>
#include <vector>

// Windows SMTC reader. The plugin polls this from its own worker thread; the
// results are turned into the `nowplaying` protocol message by bridge-server.
namespace geseki::smtc {

struct Session {
	std::string source_app_id;

	// media_properties
	std::string title;
	std::string artist;
	std::string album_title;
	std::string album_artist;
	std::string subtitle;
	int track_number = 0;
	int album_track_count = 0;
	std::vector<std::string> genres;

	// playback_info
	int playback_status = 0; // 0 closed .. 5 paused
	int playback_type = 0;   // 0 unknown, 1 music, 2 video, 3 image
	double playback_rate = 1.0;
	bool is_shuffle_active = false;
	int auto_repeat_mode = 0;

	// timeline_properties (milliseconds)
	int64_t position_ms = 0;
	int64_t start_ms = 0;
	int64_t end_ms = 0;
	int64_t min_seek_ms = 0;
	int64_t max_seek_ms = 0;
	std::string last_updated_time; // ISO-8601

	// Raw JPEG/PNG bytes of the cover art, empty when the session has none.
	// Kept raw (not base64) so the HTTP layer can cache it as a file.
	std::vector<uint8_t> thumbnail;
};

struct Snapshot {
	// App id of the session Windows currently considers focused, or empty.
	std::string current_session_id;
	std::vector<Session> sessions;
};

// True when the SMTC APIs are usable on this machine. Callers should treat a
// false result as "feature absent", not as an error to surface loudly.
bool Available();

// Reads every active session. Blocking; call from a worker thread.
Snapshot Poll();

// Sends a transport command to the focused session.
// action: play | pause | toggle | next | previous | seek (position_ms used).
bool Control(const std::string &action, int64_t position_ms);

} // namespace geseki::smtc
