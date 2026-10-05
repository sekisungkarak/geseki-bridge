#pragma once

#include <functional>
#include <string>

// Supervises the Go TikTok sidecar: starts it, keeps it alive, and exchanges
// newline-delimited JSON over its stdin/stdout (docs/protocol.md §4).
//
// The sidecar is deliberately a separate process rather than a linked library:
// the TikTok client is Go, and embedding a Go runtime in the OBS module would
// mean shipping a cgo bridge for no functional gain.
namespace geseki::tiktok {

// Called on the sidecar's reader thread for every message it emits.
// `line` is one JSON object: {"ev":"state"|"tiktok"|"ready", ...}.
using MessageHandler = std::function<void(const std::string &line)>;

// Starts the sidecar if the executable can be found next to the plugin.
// Returns false (and logs) when the binary is missing — the rest of the bridge
// keeps working, only TikTok stays unavailable.
// `signerUrl` points the TikTok client at the local sign server. Leave it
// empty to use the alternative connection mode instead; `apiKey` is only
// meaningful there, where it raises the signing rate limit.
bool Start(const std::string &username, const std::string &signerUrl,
           const std::string &apiKey, MessageHandler handler);

// Stops the sidecar: asks it to quit, then kills it if it lingers.
void Stop();

// Sends a raw JSON command line to the sidecar. No-op when not running.
void Send(const std::string &json_line);

// True while the child process is alive.
bool Running();

} // namespace geseki::tiktok
