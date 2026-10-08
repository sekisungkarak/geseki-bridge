/*
 * Geseki Bridge, OBS plugin entry point.
 *
 * Registers the plugin, owns the bridge server lifetime, and builds the Tools
 * menu. All real work lives in bridge-server.cpp (local WebSocket + HTTP),
 * smtc-winrt.cpp (media) and widget-docks.cpp (the widget dock menu).
 */
#include <obs-module.h>
#include <obs-frontend-api.h>

#include "backup.hpp"
#include "bridge-server.hpp"
#include "plugin-support.hpp"
#include "widget-docks.hpp"

OBS_DECLARE_MODULE()
OBS_MODULE_USE_DEFAULT_LOCALE("geseki-bridge", "en-US")

MODULE_EXPORT const char *obs_module_description(void)
{
	return "Geseki Bridge, one local endpoint for TikTok LIVE and Windows media, for Geseki widgets.";
}

bool obs_module_load(void)
{
	obs_log(LOG_INFO, "geseki-bridge %s loading", GESEKI_BRIDGE_VERSION);

	geseki::bridge::Start();
	geseki::docks::Setup();

	// Snapshot before obs-browser opens its Local Storage: a leveldb file in
	// use cannot be copied, so this must run at load, not on a timer.
	geseki::backup::MaybeAutoBackup();

	obs_log(LOG_INFO, "geseki-bridge ready");
	return true;
}

void obs_module_unload(void)
{
	// Stop() joins the server thread and terminates the TikTok sidecar; a
	// lingering child process would keep the port bound after OBS closes.
	geseki::bridge::Stop();
	obs_log(LOG_INFO, "geseki-bridge unloaded");
}
