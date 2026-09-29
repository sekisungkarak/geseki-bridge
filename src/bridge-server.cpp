/*
 * Geseki Bridge server.
 *
 * One loopback TCP listener that speaks:
 *   - WebSocket  (RFC 6455) at GET /ws   — the widget protocol (docs/protocol.md)
 *   - HTTP       GET /health             — liveness probe
 *   - HTTP       GET /now-playing        — legacy SMTC-Bridge compatible payload
 *   - HTTP       GET /artwork/<app_id>   — cached cover art (?v=<version>)
 *   - HTTP       GET /sessions           — Active Audio Sources page
 *
 * The SMTC half replaces the old Python "SMTC Bridge" tray app: we poll
 * geseki::smtc (WinRT) on a worker thread, diff the snapshot, and push a
 * `nowplaying` frame whenever it changes. The payload keeps the exact shape the
 * upstream SMTC Bridge exposed, so the existing widget keeps working once it
 * points at this port.
 *
 * The TikTok half forwards normalised frames from the Go sidecar (see
 * tiktok-supervisor.cpp) to every subscribed client.
 *
 * Windows only: the plugin itself only ships for Windows.
 */
#include "bridge-server.hpp"
#include "json-util.hpp"
#include "plugin-support.hpp"
#include "smtc-winrt.hpp"
#include "tiktok-supervisor.hpp"

#ifdef _WIN32

// winsock2.h must precede windows.h, which the OBS headers pull in.
#include <winsock2.h>
#include <ws2tcpip.h>
#include <windows.h>
#include <shellapi.h>

#pragma comment(lib, "ws2_32.lib")
#pragma comment(lib, "shell32.lib")

#include <obs-module.h>
#include <obs.h>

#include <algorithm>
#include <array>
#include <atomic>
#include <cctype>
#include <chrono>
#include <cstdint>
#include <cstdio>
#include <cstdlib>
#include <cstring>
#include <map>
#include <memory>
#include <mutex>
#include <set>
#include <sstream>
#include <string>
#include <thread>
#include <vector>

namespace {

using Socket = SOCKET;
const Socket kInvalidSocket = INVALID_SOCKET;

const char *kBridgeId = "geseki-bridge/" GESEKI_BRIDGE_VERSION;
const char *kGuid = "258EAFA5-E914-47DA-95CA-C5AB0DC85B11"; // RFC 6455 magic

// ------------------------------------------------------------------ globals

std::atomic<bool> g_running{false};
std::atomic<int> g_port{47800};
std::atomic<bool> g_smtc_available{false};
std::atomic<bool> g_wsa_ready{false};

Socket g_listen_sock = kInvalidSocket;
std::thread g_accept_thread;
std::thread g_smtc_thread;
std::thread g_maint_thread;

std::mutex g_cfg_mu;
geseki::bridge::Config g_cfg;

// --------------------------------------------------------------- small utils

std::string ToLower(std::string s)
{
	for (char &c : s)
		c = static_cast<char>(::tolower(static_cast<unsigned char>(c)));
	return s;
}

bool IsUnreserved(unsigned char c)
{
	return (c >= 'A' && c <= 'Z') || (c >= 'a' && c <= 'z') ||
	       (c >= '0' && c <= '9') || c == '-' || c == '_' || c == '.' || c == '~';
}

std::string UrlEncode(const std::string &in)
{
	static const char *hex = "0123456789ABCDEF";
	std::string out;
	out.reserve(in.size());
	for (unsigned char c : in) {
		if (IsUnreserved(c)) {
			out += static_cast<char>(c);
		} else {
			out += '%';
			out += hex[c >> 4];
			out += hex[c & 0x0F];
		}
	}
	return out;
}

int HexVal(char c)
{
	if (c >= '0' && c <= '9')
		return c - '0';
	if (c >= 'a' && c <= 'f')
		return c - 'a' + 10;
	if (c >= 'A' && c <= 'F')
		return c - 'A' + 10;
	return -1;
}

std::string UrlDecode(const std::string &in)
{
	std::string out;
	out.reserve(in.size());
	for (size_t i = 0; i < in.size(); ++i) {
		if (in[i] == '%' && i + 2 < in.size()) {
			const int hi = HexVal(in[i + 1]);
			const int lo = HexVal(in[i + 2]);
			if (hi >= 0 && lo >= 0) {
				out += static_cast<char>((hi << 4) | lo);
				i += 2;
				continue;
			}
		}
		if (in[i] == '+') {
			out += ' ';
			continue;
		}
		out += in[i];
	}
	return out;
}

std::string Base64Encode(const uint8_t *data, size_t len)
{
	static const char tbl[] =
		"ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789+/";
	std::string out;
	out.reserve(((len + 2) / 3) * 4);
	for (size_t i = 0; i < len; i += 3) {
		const uint32_t b0 = data[i];
		const uint32_t b1 = (i + 1 < len) ? data[i + 1] : 0;
		const uint32_t b2 = (i + 2 < len) ? data[i + 2] : 0;
		const uint32_t triple = (b0 << 16) | (b1 << 8) | b2;
		out += tbl[(triple >> 18) & 0x3F];
		out += tbl[(triple >> 12) & 0x3F];
		out += (i + 1 < len) ? tbl[(triple >> 6) & 0x3F] : '=';
		out += (i + 2 < len) ? tbl[triple & 0x3F] : '=';
	}
	return out;
}

// Minimal SHA-1, needed only for the WebSocket handshake. 40 lines of
// well-known arithmetic beat adding a crypto dependency to an OBS module.
struct Sha1 {
	uint32_t h[5] = {0x67452301u, 0xEFCDAB89u, 0x98BADCFEu, 0x10325476u, 0xC3D2E1F0u};
	uint64_t total = 0;
	uint8_t buf[64]{};
	size_t buflen = 0;

	static uint32_t Rol(uint32_t v, int b) { return (v << b) | (v >> (32 - b)); }

	void Process(const uint8_t *p)
	{
		uint32_t w[80];
		for (int i = 0; i < 16; ++i)
			w[i] = (uint32_t(p[i * 4]) << 24) | (uint32_t(p[i * 4 + 1]) << 16) |
			       (uint32_t(p[i * 4 + 2]) << 8) | uint32_t(p[i * 4 + 3]);
		for (int i = 16; i < 80; ++i)
			w[i] = Rol(w[i - 3] ^ w[i - 8] ^ w[i - 14] ^ w[i - 16], 1);

		uint32_t a = h[0], b = h[1], c = h[2], d = h[3], e = h[4];
		for (int i = 0; i < 80; ++i) {
			uint32_t f, k;
			if (i < 20) {
				f = (b & c) | ((~b) & d);
				k = 0x5A827999u;
			} else if (i < 40) {
				f = b ^ c ^ d;
				k = 0x6ED9EBA1u;
			} else if (i < 60) {
				f = (b & c) | (b & d) | (c & d);
				k = 0x8F1BBCDCu;
			} else {
				f = b ^ c ^ d;
				k = 0xCA62C1D6u;
			}
			const uint32_t t = Rol(a, 5) + f + e + k + w[i];
			e = d;
			d = c;
			c = Rol(b, 30);
			b = a;
			a = t;
		}
		h[0] += a;
		h[1] += b;
		h[2] += c;
		h[3] += d;
		h[4] += e;
	}

	void Update(const uint8_t *data, size_t len)
	{
		total += len;
		while (len > 0) {
			const size_t take = std::min(len, sizeof(buf) - buflen);
			std::memcpy(buf + buflen, data, take);
			buflen += take;
			data += take;
			len -= take;
			if (buflen == sizeof(buf)) {
				Process(buf);
				buflen = 0;
			}
		}
	}

	std::array<uint8_t, 20> Final()
	{
		const uint64_t bits = total * 8;
		const uint8_t pad = 0x80;
		Update(&pad, 1);
		const uint8_t zero = 0;
		while (buflen != 56)
			Update(&zero, 1);
		uint8_t lenb[8];
		for (int i = 0; i < 8; ++i)
			lenb[i] = uint8_t((bits >> (56 - i * 8)) & 0xFF);
		Update(lenb, 8);

		std::array<uint8_t, 20> out{};
		for (int i = 0; i < 5; ++i) {
			out[i * 4] = uint8_t(h[i] >> 24);
			out[i * 4 + 1] = uint8_t(h[i] >> 16);
			out[i * 4 + 2] = uint8_t(h[i] >> 8);
			out[i * 4 + 3] = uint8_t(h[i]);
		}
		return out;
	}
};

std::string Sha1Base64(const std::string &input)
{
	Sha1 sha;
	sha.Update(reinterpret_cast<const uint8_t *>(input.data()), input.size());
	const auto raw = sha.Final();
	return Base64Encode(raw.data(), raw.size());
}

// ------------------------------------------------------------------- sockets

void CloseSocket(Socket &s)
{
	if (s != kInvalidSocket) {
		closesocket(s);
		s = kInvalidSocket;
	}
}

bool SetNoDelay(Socket s)
{
	BOOL yes = TRUE;
	return setsockopt(s, IPPROTO_TCP, TCP_NODELAY,
			  reinterpret_cast<const char *>(&yes), sizeof(yes)) == 0;
}

// Cap how long a send may block. Broadcasts run while holding the client-list
// lock, so one wedged widget must not be able to stall every other client.
void SetSendTimeout(Socket s, int ms)
{
	DWORD t = static_cast<DWORD>(ms);
	setsockopt(s, SOL_SOCKET, SO_SNDTIMEO, reinterpret_cast<const char *>(&t),
		   sizeof(t));
}

bool RecvAll(Socket s, void *buf, size_t n)
{
	char *p = static_cast<char *>(buf);
	size_t got = 0;
	while (got < n) {
		const int r = recv(s, p + got, static_cast<int>(n - got), 0);
		if (r <= 0)
			return false;
		got += static_cast<size_t>(r);
	}
	return true;
}

bool SendAll(Socket s, const void *buf, size_t n)
{
	const char *p = static_cast<const char *>(buf);
	size_t sent = 0;
	while (sent < n) {
		const int r = send(s, p + sent, static_cast<int>(n - sent), 0);
		if (r <= 0)
			return false;
		sent += static_cast<size_t>(r);
	}
	return true;
}

// Reads one CRLF-terminated line. Caps the length so a hostile client cannot
// grow the buffer without bound.
bool RecvLine(Socket s, std::string &out, size_t max = 8192)
{
	out.clear();
	char c;
	while (out.size() < max) {
		if (!RecvAll(s, &c, 1))
			return false;
		if (c == '\n') {
			if (!out.empty() && out.back() == '\r')
				out.pop_back();
			return true;
		}
		out += c;
	}
	return false;
}

// --------------------------------------------------------------------- config

// obs_module_config_path() only *builds* the path — it never creates the
// directory. On a fresh install plugin_config/geseki-bridge/ therefore does not
// exist, and the first save died with
//   os_quick_write_utf8_file_safe: failed to write to
//   .../plugin_config/geseki-bridge/config.json.tmp
// leaving the port/username/autoconnect settings silently unsaved. Create the
// module's config directory (and any missing parents) before writing.
void EnsureParentDir(const std::string &path)
{
	const size_t slash = path.find_last_of("\\/");
	if (slash == std::string::npos || slash == 0)
		return;

	const std::string dir = path.substr(0, slash);

	// OBS hands back a UTF-8 path; the Win32 directory API wants UTF-16.
	const int n = MultiByteToWideChar(CP_UTF8, 0, dir.c_str(), -1, nullptr, 0);
	if (n <= 0)
		return;
	std::wstring w(static_cast<size_t>(n), L'\0');
	MultiByteToWideChar(CP_UTF8, 0, dir.c_str(), -1, &w[0], n);
	w.resize(static_cast<size_t>(n - 1)); // drop the trailing NUL

	// Create one component at a time so a missing intermediate directory does
	// not make the whole call fail. Components that already exist report
	// ERROR_ALREADY_EXISTS, which is not an error here.
	for (size_t i = 1; i <= w.size(); ++i) {
		const bool at_end = (i == w.size());
		if (!at_end && w[i] != L'\\' && w[i] != L'/')
			continue;
		const std::wstring part = w.substr(0, i);
		if (part.empty() || part.back() == L':')
			continue; // "E:" is a drive, not a directory to create
		CreateDirectoryW(part.c_str(), nullptr);
	}
}

std::string ConfigFilePath()
{
	char *path = obs_module_config_path("config.json");
	if (!path)
		return std::string();
	std::string p = path;
	bfree(path);
	return p;
}

// obs_data_get_string can hand back nullptr for an absent key; never let that
// reach a std::string.
std::string DataString(obs_data_t *data, const char *key)
{
	const char *v = obs_data_get_string(data, key);
	return v ? std::string(v) : std::string();
}

geseki::bridge::Config LoadConfigFile()
{
	geseki::bridge::Config cfg;
	const std::string path = ConfigFilePath();
	if (path.empty())
		return cfg;

	obs_data_t *data = obs_data_create_from_json_file_safe(path.c_str(), "bak");
	if (!data)
		return cfg;

	const long long port = obs_data_get_int(data, "port");
	if (port > 0 && port <= 65535)
		cfg.port = static_cast<int>(port);
	cfg.tiktok_username = DataString(data, "tiktok_username");
	cfg.tiktok_api_key = DataString(data, "tiktok_api_key");
	cfg.tiktok_autoconnect = obs_data_get_bool(data, "tiktok_autoconnect");
	obs_data_release(data);
	return cfg;
}

void SaveConfigFile(const geseki::bridge::Config &cfg)
{
	const std::string path = ConfigFilePath();
	if (path.empty())
		return;

	// The directory does not exist on a fresh install; without this the write
	// fails and the settings are lost without any visible error.
	EnsureParentDir(path);

	obs_data_t *data = obs_data_create();
	obs_data_set_int(data, "port", cfg.port);
	obs_data_set_string(data, "tiktok_username", cfg.tiktok_username.c_str());
	obs_data_set_string(data, "tiktok_api_key", cfg.tiktok_api_key.c_str());
	obs_data_set_bool(data, "tiktok_autoconnect", cfg.tiktok_autoconnect);
	if (!obs_data_save_json_safe(data, path.c_str(), "tmp", "bak"))
		obs_log(LOG_WARNING, "geseki-bridge: could not save config to %s", path.c_str());
	obs_data_release(data);
}

// ------------------------------------------------------------- status / subs

std::mutex g_status_mu;
std::string g_tiktok_state = "off";
std::string g_tiktok_message;
std::string g_tiktok_username;

std::string BuildHello()
{
	return std::string("{\"type\":\"hello\",\"protocol\":") +
	       std::to_string(GESEKI_BRIDGE_PROTOCOL) + ",\"bridge\":\"" + kBridgeId +
	       "\",\"capabilities\":[\"tiktok\",\"nowplaying\"]}";
}

std::string BuildStatus()
{
	std::lock_guard<std::mutex> lk(g_status_mu);
	return std::string("{\"type\":\"status\",\"tiktok\":{\"state\":\"") +
	       geseki::json::Escape(g_tiktok_state) + "\",\"username\":\"" +
	       geseki::json::Escape(g_tiktok_username) + "\",\"message\":\"" +
	       geseki::json::Escape(g_tiktok_message) +
	       "\"},\"nowplaying\":{\"state\":\"" +
	       (g_smtc_available.load() ? "connected" : "off") +
	       "\",\"message\":\"\"}}";
}

// ------------------------------------------------------------------- clients

class Client {
public:
	explicit Client(Socket s) : sock_(s) {}
	// The socket is owned by the connection thread (HandleConnection), not by
	// this object: broadcasts may still be writing when a client is reaped.

	Socket sock() const { return sock_; }
	std::mutex &write_mutex() { return wmu_; }

	// An empty subscription set means "everything".
	bool wants(const std::string &type) const
	{
		std::lock_guard<std::mutex> lk(sub_mu_);
		if (sub_.empty())
			return true;
		return sub_.count(type) > 0;
	}
	void set_subscriptions(const std::set<std::string> &s)
	{
		std::lock_guard<std::mutex> lk(sub_mu_);
		sub_ = s;
	}

	void Shutdown()
	{
		if (sock_ != kInvalidSocket)
			::shutdown(sock_, SD_BOTH);
	}

private:
	Socket sock_;
	mutable std::mutex sub_mu_;
	std::set<std::string> sub_;
	std::mutex wmu_;
};

std::mutex g_clients_mu;
std::vector<std::shared_ptr<Client>> g_clients;

// ------------------------------------------------------------- ws frame I/O

enum class FrameKind { Text, Binary, Close, Ping, Pong, Error };

bool WsSendFrame(Client &c, uint8_t opcode, const std::string &payload)
{
	std::lock_guard<std::mutex> lk(c.write_mutex());
	std::vector<uint8_t> frame;
	frame.reserve(payload.size() + 10);
	frame.push_back(static_cast<uint8_t>(0x80 | (opcode & 0x0F)));
	const size_t n = payload.size();
	if (n < 126) {
		frame.push_back(static_cast<uint8_t>(n));
	} else if (n <= 0xFFFF) {
		frame.push_back(126);
		frame.push_back(static_cast<uint8_t>(n >> 8));
		frame.push_back(static_cast<uint8_t>(n));
	} else {
		frame.push_back(127);
		for (int i = 7; i >= 0; --i)
			frame.push_back(static_cast<uint8_t>((uint64_t(n) >> (i * 8)) & 0xFF));
	}
	frame.insert(frame.end(), payload.begin(), payload.end());
	return SendAll(c.sock(), frame.data(), frame.size());
}

bool WsSendText(Client &c, const std::string &payload)
{
	return WsSendFrame(c, 0x1, payload);
}

bool WsSendPong(Client &c, const std::string &payload)
{
	if (payload.size() > 125)
		return true; // oversized control frame: drop, do not break the socket
	return WsSendFrame(c, 0xA, payload);
}

bool WsReadFrame(Socket s, FrameKind &kind, std::string &payload)
{
	uint8_t h[2];
	if (!RecvAll(s, h, 2))
		return false;

	const uint8_t opcode = h[0] & 0x0F;
	const bool masked = (h[1] & 0x80) != 0;
	uint64_t len = h[1] & 0x7F;

	if (len == 126) {
		uint8_t e[2];
		if (!RecvAll(s, e, 2))
			return false;
		len = (uint64_t(e[0]) << 8) | e[1];
	} else if (len == 127) {
		uint8_t e[8];
		if (!RecvAll(s, e, 8))
			return false;
		len = 0;
		for (int i = 0; i < 8; ++i)
			len = (len << 8) | e[i];
	}

	// Client frames are tiny (commands). Refuse anything large rather than
	// allocating on a hostile length field.
	if (len > 4ull * 1024 * 1024)
		return false;

	uint8_t mask[4] = {0, 0, 0, 0};
	if (masked && !RecvAll(s, mask, 4))
		return false;

	payload.resize(static_cast<size_t>(len));
	if (len && !RecvAll(s, &payload[0], static_cast<size_t>(len)))
		return false;

	if (masked)
		for (size_t i = 0; i < payload.size(); ++i)
			payload[i] = static_cast<char>(payload[i] ^ mask[i % 4]);

	switch (opcode) {
	case 0x1: kind = FrameKind::Text; return true;
	case 0x2: kind = FrameKind::Binary; return true;
	case 0x8: kind = FrameKind::Close; return true;
	case 0x9: kind = FrameKind::Ping; return true;
	case 0xA: kind = FrameKind::Pong; return true;
	case 0x0: kind = FrameKind::Text; return true; // continuation (unfragmented here)
	default: return false;
	}
}

void Broadcast(const std::string &json, const std::string &event_type)
{
	std::lock_guard<std::mutex> lk(g_clients_mu);
	for (auto &c : g_clients) {
		if (c->wants(event_type))
			WsSendText(*c, json);
	}
}

void RemoveClient(const std::shared_ptr<Client> &client)
{
	std::lock_guard<std::mutex> lk(g_clients_mu);
	g_clients.erase(std::remove(g_clients.begin(), g_clients.end(), client),
			g_clients.end());
}

// ------------------------------------------------------------- artwork cache

struct ArtEntry {
	std::vector<uint8_t> bytes;
	uint64_t version = 0;
};

std::mutex g_art_mu;
std::map<std::string, ArtEntry> g_art;
uint64_t g_art_seq = 0;

std::string ArtworkUrl(const std::string &app_id, uint64_t version)
{
	return "http://127.0.0.1:" + std::to_string(g_port.load()) + "/artwork/" +
	       UrlEncode(app_id) + "?v=" + std::to_string(version);
}

// Refreshes the in-memory cache and returns the version for `app_id` (0 when we
// hold no artwork for it). The version doubles as the cache-buster, and it only
// changes when the bytes change — so a widget can cache the URL aggressively.
uint64_t UpdateArtworkCache(const std::vector<geseki::smtc::Session> &sessions,
			    const std::string &app_id)
{
	std::lock_guard<std::mutex> lk(g_art_mu);
	uint64_t version = 0;
	for (const auto &s : sessions) {
		if (s.thumbnail.empty())
			continue;
		auto it = g_art.find(s.source_app_id);
		if (it == g_art.end() || it->second.bytes != s.thumbnail) {
			ArtEntry e;
			e.bytes = s.thumbnail;
			e.version = ++g_art_seq;
			it = g_art.insert_or_assign(s.source_app_id, std::move(e)).first;
		}
		if (s.source_app_id == app_id)
			version = it->second.version;
	}
	// Drop artwork for sessions that are gone so the cache cannot grow forever.
	for (auto it = g_art.begin(); it != g_art.end();) {
		bool live = false;
		for (const auto &s : sessions) {
			if (s.source_app_id == it->first && !s.thumbnail.empty()) {
				live = true;
				break;
			}
		}
		if (!live)
			it = g_art.erase(it);
		else
			++it;
	}
	return version;
}

bool FetchArtwork(const std::string &app_id, std::vector<uint8_t> &out)
{
	std::lock_guard<std::mutex> lk(g_art_mu);
	auto it = g_art.find(app_id);
	if (it == g_art.end())
		return false;
	out = it->second.bytes;
	return !out.empty();
}

// --------------------------------------------------- nowplaying json builder

std::string BuildNowPlayingData(const geseki::smtc::Snapshot &snap)
{
	std::string s;
	s.reserve(512);
	s += "{";
	s += geseki::json::Str("app_version", GESEKI_BRIDGE_VERSION);
	s += ",";
	s += geseki::json::Str("current_session_id", snap.current_session_id);
	s += ",\"sessions\":[";
	for (size_t i = 0; i < snap.sessions.size(); ++i) {
		const auto &sess = snap.sessions[i];
		if (i)
			s += ",";
		s += "{";
		s += geseki::json::Str("source_app_id", sess.source_app_id);

		s += ",\"media_properties\":{";
		s += geseki::json::Str("Title", sess.title) + ",";
		s += geseki::json::Str("Artist", sess.artist) + ",";
		s += geseki::json::Str("AlbumTitle", sess.album_title) + ",";
		s += geseki::json::Str("AlbumArtist", sess.album_artist) + ",";
		// Artwork is served by our own HTTP layer rather than embedded as
		// base64: the widget accepts a URL just as well, and a URL keeps every
		// poll small.
		std::string thumb;
		if (!sess.thumbnail.empty()) {
			const uint64_t v = UpdateArtworkCache(snap.sessions, sess.source_app_id);
			thumb = ArtworkUrl(sess.source_app_id, v);
		}
		s += geseki::json::Str("Thumbnail", thumb) + ",";
		s += geseki::json::Num("AlbumTrackCount",
				       static_cast<int64_t>(sess.album_track_count)) +
		     ",";
		s += geseki::json::Num("TrackNumber", static_cast<int64_t>(sess.track_number)) +
		     ",";
		s += "\"Genres\":[";
		for (size_t g = 0; g < sess.genres.size(); ++g) {
			if (g)
				s += ",";
			s += "\"" + geseki::json::Escape(sess.genres[g]) + "\"";
		}
		s += "],";
		s += geseki::json::Str("Subtitle", sess.subtitle);
		s += "}";

		s += ",\"playback_info\":{";
		s += geseki::json::Num("PlaybackStatus",
				       static_cast<int64_t>(sess.playback_status)) +
		     ",";
		s += geseki::json::Num("PlaybackType",
				       static_cast<int64_t>(sess.playback_type)) +
		     ",";
		s += geseki::json::Num("PlaybackRate", sess.playback_rate) + ",";
		s += geseki::json::Bool("IsShuffleActive", sess.is_shuffle_active) + ",";
		s += geseki::json::Num("AutoRepeatMode",
				       static_cast<int64_t>(sess.auto_repeat_mode));
		s += "}";

		s += ",\"timeline_properties\":{";
		s += geseki::json::Num("Position", sess.position_ms) + ",";
		s += geseki::json::Num("StartTime", sess.start_ms) + ",";
		s += geseki::json::Num("EndTime", sess.end_ms) + ",";
		s += geseki::json::Num("MinSeekTime", sess.min_seek_ms) + ",";
		s += geseki::json::Num("MaxSeekTime", sess.max_seek_ms) + ",";
		s += geseki::json::Str("LastUpdatedTime", sess.last_updated_time);
		s += "}";

		s += "}";
	}
	s += "]}";
	return s;
}

// Last payload, so /now-playing answers instantly and a client that connects
// mid-song is not blank until the next change.
std::mutex g_np_mu;
std::string g_np_json;
std::string g_np_data_json;

bool AnyPlaying(const geseki::smtc::Snapshot &snap)
{
	for (const auto &s : snap.sessions)
		if (s.playback_status == 4)
			return true;
	return false;
}

// ------------------------------------------------------------- tiktok wiring

std::mutex g_tt_mu;
bool g_tt_should_run = false;
std::string g_tt_user;
std::string g_tt_key;

// Serialises calls into the supervisor: the maintenance thread may be inside
// tiktok::Start() when Stop() runs, and the supervisor is not re-entrant.
std::mutex g_tt_call_mu;

std::string JsonStrField(const geseki::json::Value &o, const char *key)
{
	const auto *v = o.find(key);
	return (v && v->is_string()) ? v->text : std::string();
}

void SetTikTokStatus(const std::string &state, const std::string &message)
{
	{
		std::lock_guard<std::mutex> lk(g_status_mu);
		g_tiktok_state = state;
		g_tiktok_message = message;
	}
	Broadcast(BuildStatus(), "status");
}

void OnSidecarMessage(const std::string &line)
{
	geseki::json::Value v;
	if (!geseki::json::Value::Parse(line, v) || !v.is_object())
		return;

	const std::string ev = JsonStrField(v, "ev");
	if (ev == "tiktok") {
		// Re-wrap the sidecar frame {"ev":"tiktok",...} into the public shape
		// {"type":"tiktok",...}. Re-serialising (rather than string-splicing)
		// guarantees the forwarded payload is well-formed.
		std::string msg = "{\"type\":\"tiktok\"";
		if (const auto *e = v.find("event"); e && e->is_string())
			msg += ",\"event\":\"" + geseki::json::Escape(e->text) + "\"";
		if (const auto *d = v.find("data"))
			msg += ",\"data\":" + geseki::json::Serialize(*d);
		msg += "}";
		Broadcast(msg, "tiktok");
		return;
	}
	if (ev == "state")
		SetTikTokStatus(JsonStrField(v, "state"), JsonStrField(v, "message"));
}

// Starts (or restarts) the sidecar and records the intent so the watchdog can
// bring it back if it dies.
void StartSidecar(const std::string &username, const std::string &apiKey)
{
	{
		std::lock_guard<std::mutex> lk(g_tt_mu);
		g_tt_should_run = !username.empty();
		g_tt_user = username;
		g_tt_key = apiKey;
	}
	if (username.empty()) {
		std::lock_guard<std::mutex> lk(g_tt_call_mu);
		geseki::tiktok::Stop();
		SetTikTokStatus("off", "");
		return;
	}
	SetTikTokStatus("connecting", "");
	{
		std::lock_guard<std::mutex> lk(g_tt_call_mu);
		geseki::tiktok::Start(username, apiKey, OnSidecarMessage);
	}
}

void StopSidecar()
{
	{
		std::lock_guard<std::mutex> lk(g_tt_mu);
		g_tt_should_run = false;
	}
	{
		std::lock_guard<std::mutex> lk(g_tt_call_mu);
		geseki::tiktok::Stop();
	}
	SetTikTokStatus("off", "");
}

void HandleTikTokConnect(const std::string &username, const std::string &apiKey)
{
	geseki::bridge::Config c;
	{
		std::lock_guard<std::mutex> lk(g_cfg_mu);
		c = g_cfg;
	}
	if (!username.empty())
		c.tiktok_username = username;
	if (!apiKey.empty())
		c.tiktok_api_key = apiKey;

	{
		std::lock_guard<std::mutex> lk(g_cfg_mu);
		g_cfg = c;
	}
	SaveConfigFile(c);
	{
		std::lock_guard<std::mutex> lk(g_status_mu);
		g_tiktok_username = c.tiktok_username;
	}
	StartSidecar(c.tiktok_username, c.tiktok_api_key);
}

// ------------------------------------------------------------ client messages

void HandleClientMessage(Client &client, const std::string &text)
{
	geseki::json::Value v;
	if (!geseki::json::Value::Parse(text, v) || !v.is_object())
		return;

	const std::string type = JsonStrField(v, "type");

	if (type == "ping") {
		const auto *t = v.find("t");
		const std::string msg =
			"{\"type\":\"pong\",\"t\":" + std::to_string(t ? t->as_int() : 0) + "}";
		WsSendText(client, msg);
		return;
	}

	if (type == "subscribe") {
		std::set<std::string> subs;
		if (const auto *ev = v.find("events"); ev && ev->is_array())
			for (const auto &e : ev->items)
				if (e.is_string())
					subs.insert(e.text);
		client.set_subscriptions(subs);
		return;
	}

	if (type == "tiktok.connect") {
		HandleTikTokConnect(JsonStrField(v, "username"), JsonStrField(v, "apiKey"));
		return;
	}

	if (type == "tiktok.disconnect") {
		StopSidecar();
		return;
	}

	if (type == "smtc.control") {
		const std::string action = JsonStrField(v, "action");
		int64_t position = 0;
		if (const auto *p = v.find("position"); p && p->is_number())
			position = p->as_int();
		if (!action.empty())
			geseki::smtc::Control(action, position);
		return;
	}
}

// ------------------------------------------------------------------ http I/O

std::string StatusText(int code)
{
	switch (code) {
	case 200: return "OK";
	case 204: return "No Content";
	case 400: return "Bad Request";
	case 404: return "Not Found";
	case 405: return "Method Not Allowed";
	default: return "OK";
	}
}

void SendHttp(Socket s, int code, const std::string &ctype, const std::string &body,
	      const std::string &extra = std::string())
{
	std::string h = "HTTP/1.1 " + std::to_string(code) + " " + StatusText(code) + "\r\n";
	h += "Content-Type: " + ctype + "\r\n";
	h += "Content-Length: " + std::to_string(body.size()) + "\r\n";
	h += "Access-Control-Allow-Origin: *\r\n";
	h += "Cache-Control: no-store\r\n";
	h += "Connection: close\r\n";
	h += extra;
	h += "\r\n";
	h += body;
	SendAll(s, h.data(), h.size());
}

std::string ContentTypeForImage(const std::vector<uint8_t> &bytes)
{
	if (bytes.size() >= 8 && bytes[0] == 0x89 && bytes[1] == 'P' && bytes[2] == 'N' &&
	    bytes[3] == 'G')
		return "image/png";
	if (bytes.size() >= 3 && bytes[0] == 0xFF && bytes[1] == 0xD8 && bytes[2] == 0xFF)
		return "image/jpeg";
	if (bytes.size() >= 4 && bytes[0] == 'G' && bytes[1] == 'I' && bytes[2] == 'F')
		return "image/gif";
	if (bytes.size() >= 12 && bytes[8] == 'W' && bytes[9] == 'E' && bytes[10] == 'B')
		return "image/webp";
	return "application/octet-stream";
}

// The Active Audio Sources page, matching the old SMTC Bridge /sessions page:
// a plain list of the app ids (source_app_user_model_id) of every active media
// session. Kept deliberately identical in content and markup.
std::string SessionsPage()
{
	std::string apps;
	{
		const auto snap = geseki::smtc::Poll();
		std::set<std::string> seen;
		for (const auto &s : snap.sessions) {
			if (!seen.insert(s.source_app_id).second)
				continue;
			apps += "<li style='margin-bottom: 8px; font-size: 1.1em;'>" +
				geseki::json::Escape(s.source_app_id) + "</li>";
		}
	}

	std::string html;
	html += "<body style='background-color: #121212; color: white; "
		"font-family: sans-serif; padding: 20px;'>";
	html += "<h3 style='margin-top: 0;'>Active Audio Sources:</h3><ul>";
	html += apps.empty()
			? "<li style='color: #888;'>No active audio sources found.</li>"
			: apps;
	html += "</ul></body>";
	return html;
}

bool HandleWebSocketUpgrade(Socket s, const std::map<std::string, std::string> &headers)
{
	auto it = headers.find("sec-websocket-key");
	if (it == headers.end() || it->second.empty())
		return false;

	const std::string accept = Sha1Base64(it->second + kGuid);
	const std::string resp = "HTTP/1.1 101 Switching Protocols\r\n"
				 "Upgrade: websocket\r\n"
				 "Connection: Upgrade\r\n"
				 "Sec-WebSocket-Accept: " +
				 accept + "\r\n\r\n";
	return SendAll(s, resp.data(), resp.size());
}

} // namespace

// ------------------------------------------------------------ worker threads

namespace {

// One accepted connection: an HTTP request/response, or a WebSocket session
// that stays open. `done` lets the accept thread reap finished handlers; `sock`
// lets Stop() unblock a handler still waiting on recv().
struct ConnThread {
	std::thread th;
	std::shared_ptr<std::atomic<bool>> done;
	Socket sock = kInvalidSocket;
};

std::mutex g_cthreads_mu;
std::vector<ConnThread> g_cthreads;

void ReapConnThreads()
{
	std::lock_guard<std::mutex> lk(g_cthreads_mu);
	for (auto it = g_cthreads.begin(); it != g_cthreads.end();) {
		if (it->done && it->done->load()) {
			if (it->th.joinable())
				it->th.join();
			it = g_cthreads.erase(it);
		} else {
			++it;
		}
	}
}

void ClientLoop(std::shared_ptr<Client> client)
{
	{
		std::lock_guard<std::mutex> lk(g_clients_mu);
		g_clients.push_back(client);
	}

	WsSendText(*client, BuildHello());
	WsSendText(*client, BuildStatus());

	{
		std::string snapshot;
		{
			std::lock_guard<std::mutex> lk(g_np_mu);
			snapshot = g_np_json;
		}
		if (!snapshot.empty() && client->wants("nowplaying"))
			WsSendText(*client, snapshot);
	}

	FrameKind kind = FrameKind::Error;
	std::string payload;
	while (g_running.load() && WsReadFrame(client->sock(), kind, payload)) {
		if (kind == FrameKind::Close)
			break;
		if (kind == FrameKind::Ping) {
			WsSendPong(*client, payload);
			continue;
		}
		if (kind == FrameKind::Pong)
			continue;
		if (kind == FrameKind::Text)
			HandleClientMessage(*client, payload);
	}

	RemoveClient(client);
}

void HandleConnection(Socket s, std::shared_ptr<std::atomic<bool>> done)
{
	std::string line;
	if (RecvLine(s, line)) {
		std::istringstream ss(line);
		std::string method, target;
		ss >> method >> target;

		std::map<std::string, std::string> headers;
		while (RecvLine(s, line)) {
			if (line.empty())
				break;
			const auto colon = line.find(':');
			if (colon == std::string::npos)
				continue;
			std::string key = ToLower(line.substr(0, colon));
			std::string val = line.substr(colon + 1);
			while (!val.empty() && (val.front() == ' ' || val.front() == '\t'))
				val.erase(val.begin());
			while (!val.empty() && (val.back() == ' ' || val.back() == '\t'))
				val.pop_back();
			headers[key] = val;
		}

		if (!method.empty() && !target.empty()) {
			std::string path = target;
			if (const auto q = target.find('?'); q != std::string::npos)
				path = target.substr(0, q);

			const bool wants_upgrade =
				headers.count("upgrade") &&
				ToLower(headers["upgrade"]).find("websocket") != std::string::npos;

			if (wants_upgrade) {
				if (HandleWebSocketUpgrade(s, headers))
					ClientLoop(std::make_shared<Client>(s));
				else
					SendHttp(s, 400, "text/plain", "bad websocket handshake");
			} else if (method == "OPTIONS") {
				SendHttp(s, 204, "text/plain", "",
					 "Access-Control-Allow-Methods: GET, POST, OPTIONS\r\n"
					 "Access-Control-Allow-Headers: Content-Type\r\n");
			} else if (path == "/health") {
				const std::string body =
					std::string("{\"ok\":true,\"bridge\":\"") + kBridgeId +
					"\",\"protocol\":" +
					std::to_string(GESEKI_BRIDGE_PROTOCOL) + "}";
				SendHttp(s, 200, "application/json", body);
			} else if (path == "/now-playing") {
				std::string data;
				{
					std::lock_guard<std::mutex> lk(g_np_mu);
					data = g_np_data_json;
				}
				if (data.empty())
					data = "{\"app_version\":\"" GESEKI_BRIDGE_VERSION
					       "\",\"current_session_id\":\"\",\"sessions\":[]}";
				SendHttp(s, 200, "application/json", data);
			} else if (path.rfind("/artwork/", 0) == 0) {
				std::vector<uint8_t> bytes;
				if (!FetchArtwork(UrlDecode(path.substr(9)), bytes)) {
					SendHttp(s, 404, "text/plain", "no artwork");
				} else {
					const std::string extra =
						"Cache-Control: public, max-age=31536000, immutable\r\n";
					SendHttp(s, 200, ContentTypeForImage(bytes),
						 std::string(bytes.begin(), bytes.end()), extra);
				}
			} else if (path == "/sessions" || path == "/") {
				SendHttp(s, 200, "text/html; charset=utf-8", SessionsPage());
			} else {
				SendHttp(s, 404, "text/plain", "not found");
			}
		}
	}

	CloseSocket(s);
	if (done)
		done->store(true);
}

void SmtcLoop()
{
	using clock = std::chrono::steady_clock;
	std::string last;
	auto last_push = clock::now();

	while (g_running.load()) {
		const auto snap = geseki::smtc::Poll();
		const std::string data = BuildNowPlayingData(snap);
		const std::string msg = "{\"type\":\"nowplaying\",\"data\":" + data + "}";

		{
			std::lock_guard<std::mutex> lk(g_np_mu);
			g_np_json = msg;
			g_np_data_json = data;
		}

		const bool changed = (data != last);
		const bool heartbeat = std::chrono::duration_cast<std::chrono::milliseconds>(
					       clock::now() - last_push)
					       .count() >= 1000;
		// Push on change; while something is playing, also keep a ~1 s beat so
		// the widget's progress bar advances even if SMTC reports the same
		// rounded position twice.
		if (changed || (AnyPlaying(snap) && heartbeat)) {
			Broadcast(msg, "nowplaying");
			last = data;
			last_push = clock::now();
		}

		for (int i = 0; i < 20 && g_running.load(); ++i)
			std::this_thread::sleep_for(std::chrono::milliseconds(50));
	}
}

// Keeps the TikTok sidecar alive: restarts it if it dies while we still want a
// connection (e.g. the stream ended and the binary exited).
void MaintenanceLoop()
{
	int failures = 0;
	while (g_running.load()) {
		for (int i = 0; i < 40 && g_running.load(); ++i)
			std::this_thread::sleep_for(std::chrono::milliseconds(50));

		bool want = false;
		std::string user, key;
		{
			std::lock_guard<std::mutex> lk(g_tt_mu);
			want = g_tt_should_run;
			user = g_tt_user;
			key = g_tt_key;
		}
		if (!want || user.empty() || geseki::tiktok::Running())
			continue;

		// Back off after repeated failures so a missing binary does not spam
		// the log twice a second forever.
		if (failures >= 3) {
			for (int i = 0; i < 200 && g_running.load(); ++i)
				std::this_thread::sleep_for(std::chrono::milliseconds(50));
		}
		obs_log(LOG_INFO, "geseki-bridge: (re)starting TikTok sidecar");
		{
			std::lock_guard<std::mutex> lk(g_tt_call_mu);
			if (geseki::tiktok::Start(user, key, OnSidecarMessage))
				failures = 0;
			else
				++failures;
		}
	}
}

void AcceptLoop()
{
	while (g_running.load()) {
		ReapConnThreads();

		fd_set rf;
		FD_ZERO(&rf);
		FD_SET(g_listen_sock, &rf);
		timeval tv{};
		tv.tv_usec = 200000; // 200 ms so Stop() is noticed promptly
		const int sel = select(0, &rf, nullptr, nullptr, &tv);
		if (!g_running.load())
			break;
		if (sel <= 0)
			continue;

		Socket s = accept(g_listen_sock, nullptr, nullptr);
		if (s == kInvalidSocket)
			continue;
		SetNoDelay(s);
		SetSendTimeout(s, 5000);

		auto done = std::make_shared<std::atomic<bool>>(false);
		std::lock_guard<std::mutex> lk(g_cthreads_mu);
		g_cthreads.push_back(ConnThread{
			std::thread([s, done] { HandleConnection(s, done); }), done, s});
	}
}

} // namespace

// ---------------------------------------------------------------- public API

namespace geseki::bridge {

void Start()
{
	if (g_running.exchange(true))
		return; // already running (idempotent)

	geseki::bridge::Config cfg;
	{
		std::lock_guard<std::mutex> lk(g_cfg_mu);
		g_cfg = LoadConfigFile();
		cfg = g_cfg;
	}

	WSADATA wsa{};
	if (WSAStartup(MAKEWORD(2, 2), &wsa) != 0) {
		obs_log(LOG_ERROR, "geseki-bridge: WSAStartup failed");
		g_running.store(false);
		return;
	}
	g_wsa_ready.store(true);

	Socket s = socket(AF_INET, SOCK_STREAM, IPPROTO_TCP);
	if (s == kInvalidSocket) {
		obs_log(LOG_ERROR, "geseki-bridge: socket() failed (%d)", WSAGetLastError());
		g_running.store(false);
		return;
	}
	// SO_EXCLUSIVEADDRUSE (not SO_REUSEADDR) so another local process cannot
	// silently steal the port on Windows.
	BOOL excl = TRUE;
	setsockopt(s, SOL_SOCKET, SO_EXCLUSIVEADDRUSE,
		   reinterpret_cast<const char *>(&excl), sizeof(excl));

	sockaddr_in addr{};
	addr.sin_family = AF_INET;
	addr.sin_port = htons(static_cast<u_short>(cfg.port));
	addr.sin_addr.s_addr = htonl(INADDR_LOOPBACK);

	if (bind(s, reinterpret_cast<sockaddr *>(&addr), sizeof(addr)) != 0 ||
	    listen(s, SOMAXCONN) != 0) {
		obs_log(LOG_ERROR, "geseki-bridge: cannot bind 127.0.0.1:%d (%d)", cfg.port,
			WSAGetLastError());
		CloseSocket(s);
		g_running.store(false);
		return;
	}
	g_listen_sock = s;
	g_port.store(cfg.port);

	g_smtc_available.store(geseki::smtc::Available());

	g_accept_thread = std::thread(AcceptLoop);
	g_smtc_thread = std::thread(SmtcLoop);
	g_maint_thread = std::thread(MaintenanceLoop);

	{
		std::lock_guard<std::mutex> lk(g_status_mu);
		g_tiktok_username = cfg.tiktok_username;
	}

	if (cfg.tiktok_autoconnect && !cfg.tiktok_username.empty())
		StartSidecar(cfg.tiktok_username, cfg.tiktok_api_key);

	obs_log(LOG_INFO, "geseki-bridge: listening on ws://127.0.0.1:%d/ws (SMTC %s)",
		cfg.port, g_smtc_available.load() ? "available" : "unavailable");
}

void Stop()
{
	g_running.store(false);

	{
		std::lock_guard<std::mutex> lk(g_tt_mu);
		g_tt_should_run = false;
	}
	{
		std::lock_guard<std::mutex> lk(g_tt_call_mu);
		geseki::tiktok::Stop();
	}

	// Wake every open socket so its recv() returns; the handlers then unwind.
	{
		std::lock_guard<std::mutex> lk(g_clients_mu);
		for (auto &c : g_clients)
			c->Shutdown();
	}

	// Join the accept thread first: it owns g_cthreads and the listen socket,
	// so nothing new can be queued while we drain.
	if (g_accept_thread.joinable())
		g_accept_thread.join();

	{
		std::lock_guard<std::mutex> lk(g_cthreads_mu);
		for (auto &ct : g_cthreads) {
			if (ct.sock != kInvalidSocket)
				::shutdown(ct.sock, SD_BOTH);
			if (ct.th.joinable())
				ct.th.join();
		}
		g_cthreads.clear();
	}

	if (g_smtc_thread.joinable())
		g_smtc_thread.join();
	if (g_maint_thread.joinable())
		g_maint_thread.join();

	CloseSocket(g_listen_sock);

	{
		std::lock_guard<std::mutex> lk(g_clients_mu);
		g_clients.clear();
	}
	{
		std::lock_guard<std::mutex> lk(g_art_mu);
		g_art.clear();
	}
	{
		std::lock_guard<std::mutex> lk(g_np_mu);
		g_np_json.clear();
		g_np_data_json.clear();
	}

	if (g_wsa_ready.exchange(false))
		WSACleanup();
}

Config GetConfig()
{
	std::lock_guard<std::mutex> lk(g_cfg_mu);
	return g_cfg;
}

void SaveConfig(const Config &cfg)
{
	Config old;
	{
		std::lock_guard<std::mutex> lk(g_cfg_mu);
		old = g_cfg;
		g_cfg = cfg;
	}
	SaveConfigFile(cfg);

	if (cfg.tiktok_username != old.tiktok_username ||
	    cfg.tiktok_api_key != old.tiktok_api_key) {
		if (!cfg.tiktok_username.empty())
			StartSidecar(cfg.tiktok_username, cfg.tiktok_api_key);
		else
			StopSidecar();
	}
	if (cfg.port != old.port)
		obs_log(LOG_WARNING, "geseki-bridge: port changed to %d; restart OBS to apply",
			cfg.port);
}

} // namespace geseki::bridge

#else // !_WIN32

namespace geseki::bridge {

void Start() {}
void Stop() {}
Config GetConfig() { return {}; }
void SaveConfig(const Config &) {}

} // namespace geseki::bridge

#endif
