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
	// When true (the default) the sidecar connects through the signature-free
	// direct WebSocket, which needs no sign server and no third-party quota.
	// Turning it off skips that path and lets the sidecar use the /connect
	// endpoint and then the optional signer, so a user can force the older
	// route if the direct one ever misbehaves. Editable in the settings dialog.
	bool tiktok_direct = true;
	// Retained for config compatibility. The plugin no longer starts a local
	// sign server: room data comes from a signature-free endpoint, and the
	// optional tiktok_api_key is the only fallback. The field is still read
	// and written so existing config files keep working unchanged.
	bool alt_connection = false;
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
