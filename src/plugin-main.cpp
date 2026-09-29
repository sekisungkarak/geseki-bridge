/*
 * Geseki Bridge — OBS plugin entry point.
 *
 * Registers the plugin, owns the bridge server lifetime, and adds a Tools
 * menu entry that opens the native settings dialog. All real work lives in
 * bridge-server.cpp (local WebSocket + HTTP) and smtc-winrt.cpp (media).
 */
#include <obs-module.h>
#include <obs-frontend-api.h>

#include "bridge-server.hpp"
#include "plugin-support.hpp"

#ifdef GESEKI_HAS_QT
#include "settings-dialog.hpp"
#endif

OBS_DECLARE_MODULE()
OBS_MODULE_USE_DEFAULT_LOCALE("geseki-bridge", "en-US")

MODULE_EXPORT const char *obs_module_description(void)
{
	return "Geseki Bridge — one local endpoint for TikTok LIVE and Windows media, for Geseki widgets.";
}

static void open_settings(void * /*data*/)
{
#ifdef GESEKI_HAS_QT
	// Pass the OBS main window so the dialog is parented (centred on OBS and
	// modal to it). obs_frontend_get_main_window returns a QMainWindow* as
	// void*, so no Qt type leaks into this translation unit.
	geseki::ui::ShowSettingsDialog(obs_frontend_get_main_window());
#endif
}

bool obs_module_load(void)
{
	obs_log(LOG_INFO, "geseki-bridge %s loading", GESEKI_BRIDGE_VERSION);

	geseki::bridge::Start();

	obs_frontend_add_tools_menu_item(
		obs_module_text("GesekiBridge.MenuItem"),
		open_settings, nullptr);

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
