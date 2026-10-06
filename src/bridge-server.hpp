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
	// Alternative Connection Mode: sign through a remote signing service
	// instead of the local sign server. Off by default; that service is
	// subject to a shared rate limit, which tiktok_api_key can raise.
	bool alt_connection = false;
	// Port of the local sign server (needs Node.js on PATH and Chrome
	// installed). Unused while the alternative mode is on.
	int sign_server_port = 8090;
};

// Starts the server and the TikTok sidecar. Idempotent.
void Start();

// Stops the server, joins its threads and terminates the sidecar. Idempotent.
void Stop();

// Absolute path of this plugin's config directory
// (...\plugin_config\geseki-bridge). Used by the backup engine.
std::string ModuleConfigDir();

// Current configuration, loaded from the plugin's config file.
Config GetConfig();

// Persists configuration and applies what can be applied live (the listening
// port needs a restart; the TikTok credentials do not).
void SaveConfig(const Config &cfg);

} // namespace geseki::bridge
