/*
 * Geseki Bridge — the widget dock and the Tools menu.
 *
 *   Tools > Geseki > Geseki Bridge…                        (settings dialog)
 *
 * The dock is a CEF panel built by obs-browser and handed to OBS with
 * obs_frontend_add_dock_by_id(). That is the difference that matters here: a
 * dock registered this way belongs to the plugin, so OBS lists it in its own
 * Docks menu but never in the user's "Custom Browser Docks" list — it cannot be
 * deleted from the UI, only hidden (the X button) and shown again from Docks.
 *
 * The earlier approach wrote an entry into `[BasicWindow] ExtraBrowserDocks`,
 * the same list the "Custom Browser Docks" dialog edits. That made the dock
 * deletable, so that entry is migrated away on load (RemoveLegacyEntry).
 *
 * Visibility is the plugin's own choice, because OBS restores its saved dock
 * layout *before* the plugin adds this dock, so the layout never carries it:
 *
 *  - `[GesekiBridge] DockVisible` holds the user's choice and defaults to
 *    false, so a fresh install starts with the dock hidden (a visible dock
 *    would cover the scene).
 *  - It is applied on the next event-loop turn, not inside the
 *    FINISHED_LOADING callback: OBS is still finishing its own window and dock
 *    setup at that point, and a setVisible() from there does not stick.
 *  - Every later change — OBS's Docks menu, the dock's X button — is written
 *    back through visibilityChanged, so the choice survives a restart.
 */
#include "widget-docks.hpp"

#include <obs-module.h>
#include <obs-frontend-api.h>
#include <util/config-file.h>

#include <string>

#include "json-util.hpp"
#include "obs-browser.hpp"
#include "plugin-support.hpp"

#ifdef GESEKI_HAS_QT
#include <QAction>
#include <QDockWidget>
#include <QMainWindow>
#include <QMenu>
#include <QPointer>
#include <QTimer>

#include "settings-dialog.hpp"
#endif

namespace geseki::docks {

namespace {

// Dock id and title. The id is fixed so repeated runs reuse one identity, and
// OBS rejects a second dock that claims the same object name.
constexpr const char *kDockId = "gesekiDynamicIslandAlertDock";
constexpr const char *kDockTitle = "Dynamic Island Alert";
constexpr const char *kDockUrl = "https://sekisungkarak.web.id/dynamic-island-alert/dashboard/";

// Default dock size. OBS creates a plugin dock with no size of its own and a
// CEF panel's sizeHint is tiny, so without this the dock opens too small to use.
constexpr int kDockWidth = 550;
constexpr int kDockHeight = 900;

// Where the user's show/hide choice is stored. Absent means hidden.
constexpr const char *kSection = "GesekiBridge";
constexpr const char *kVisibleKey = "DockVisible";

// Drops every entry whose "title" is kDockTitle, so the dock no longer appears
// in the user's Custom Browser Docks list (where it could be deleted). Leaves
// the rest of the user's entries untouched.
void RemoveLegacyEntry()
{
	config_t *config = obs_frontend_get_user_config();
	if (!config)
		return;

	const char *raw = config_get_string(config, "BasicWindow", "ExtraBrowserDocks");
	if (!raw || !*raw)
		return;

	json::Value list;
	std::string error;
	if (!json::Value::Parse(raw, list, &error)) {
		obs_log(LOG_WARNING, "ExtraBrowserDocks is not valid JSON (%s); leaving it alone",
			error.c_str());
		return;
	}
	if (list.type != json::Value::Type::Array)
		return;

	json::Value kept;
	kept.type = json::Value::Type::Array;
	bool removed = false;
	for (const json::Value &item : list.items) {
		const json::Value *title = item.find("title");
		if (title && title->as_string() == kDockTitle) {
			removed = true;
			continue;
		}
		kept.items.push_back(item);
	}

	if (!removed)
		return;

	const std::string serialized = json::Serialize(kept);
	config_set_string(config, "BasicWindow", "ExtraBrowserDocks", serialized.c_str());
	config_save(config);

	obs_log(LOG_INFO, "removed legacy Custom Browser Dock entry '%s'", kDockTitle);
}

// The user's stored choice; hidden unless they turned it on.
bool DockVisiblePref()
{
	config_t *config = obs_frontend_get_user_config();
	return config && config_get_bool(config, kSection, kVisibleKey);
}

void SaveDockVisiblePref(bool visible)
{
	config_t *config = obs_frontend_get_user_config();
	if (!config)
		return;

	config_set_bool(config, kSection, kVisibleKey, visible);
	config_save(config);
}

#ifdef GESEKI_HAS_QT

QPointer<QDockWidget> g_dock;

void OnFrontendEvent(enum obs_frontend_event event, void * /*data*/)
{
	// The frontend exists by now: the main window is up and the browser panel
	// is available, so the dock can be created.
	if (event != OBS_FRONTEND_EVENT_FINISHED_LOADING)
		return;

	if (g_dock)
		return;

	QCef *cef = geseki::browser::Panel();
	if (!cef) {
		obs_log(LOG_WARNING, "obs-browser is unavailable; the widget dock was not created");
		return;
	}

	QWidget *browser = cef->create_widget(nullptr, kDockUrl);
	if (!browser) {
		obs_log(LOG_WARNING, "could not create the widget browser panel");
		return;
	}

	// A plugin-owned dock: OBS gives it a Docks menu entry and a close button,
	// but it is not one of the user's Custom Browser Docks, so the Custom
	// Browser Docks dialog cannot delete it.
	if (!obs_frontend_add_dock_by_id(kDockId, kDockTitle, browser)) {
		obs_log(LOG_WARNING, "could not register the widget dock");
		return;
	}

	auto *window = static_cast<QMainWindow *>(obs_frontend_get_main_window());
	g_dock = window ? window->findChild<QDockWidget *>(QString::fromUtf8(kDockId)) : nullptr;
	if (!g_dock) {
		obs_log(LOG_WARNING, "widget dock '%s' was not created by OBS", kDockTitle);
		return;
	}

	// Apply the stored choice once OBS has finished its own startup work, then
	// start tracking changes so the choice survives the next restart.
	const bool want = DockVisiblePref();
	QTimer::singleShot(0, g_dock, [want] {
		if (!g_dock)
			return;

		// OBS gives a plugin dock no size of its own and a CEF panel reports a
		// tiny sizeHint, so it would otherwise open too small to use.
		g_dock->resize(kDockWidth, kDockHeight);

		g_dock->setVisible(want);

		QObject::connect(g_dock, &QDockWidget::visibilityChanged, g_dock,
				 [](bool visible) { SaveDockVisiblePref(visible); });

		obs_log(LOG_INFO, "widget dock visibility applied: %s", want ? "shown" : "hidden");
	});
}

void OpenSettings()
{
	geseki::ui::ShowSettingsDialog(obs_frontend_get_main_window());
}

#endif // GESEKI_HAS_QT

} // namespace

void Setup()
{
	// The dock used to be a Custom Browser Dock; drop that entry so it stops
	// showing up in the dialog that could delete it.
	RemoveLegacyEntry();

#ifdef GESEKI_HAS_QT
	auto *root_action = static_cast<QAction *>(
		obs_frontend_add_tools_menu_qaction(obs_module_text("GesekiBridge.MenuItem")));
	if (!root_action) {
		obs_log(LOG_WARNING, "could not add the Geseki menu");
		return;
	}

	auto *window = static_cast<QMainWindow *>(obs_frontend_get_main_window());
	QMenu *root = new QMenu(root_action->text(), window);
	root_action->setMenu(root);

	QAction *settings = root->addAction(obs_module_text("GesekiBridge.SettingsItem"));
	QObject::connect(settings, &QAction::triggered, root, [] { OpenSettings(); });

	obs_frontend_add_event_callback(OnFrontendEvent, nullptr);
#endif
}

} // namespace geseki::docks
