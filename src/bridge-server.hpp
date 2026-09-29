#pragma once

#include <string>

// Geseki Bridge server: one loopback WebSocket endpoint that every Geseki
// widget connects to, plus a small HTTP surface for health, cover art and the
// Active Audio Sources page. See docs/protocol.md.
namespace geseki::bridge {

struct Config {
	int port = 47800;
	std::string tiktok_username;
	std::string tiktok_api_key;
	bool tiktok_autoconnect = false;
};

// Starts the server and the TikTok sidecar. Idempotent.
void Start();

// Stops the server, joins its threads and terminates the sidecar. Idempotent.
void Stop();

// Current configuration, loaded from the plugin's config file.
Config GetConfig();

// Persists configuration and applies what can be applied live (the listening
// port needs a restart; the TikTok credentials do not).
void SaveConfig(const Config &cfg);

} // namespace geseki::bridge
