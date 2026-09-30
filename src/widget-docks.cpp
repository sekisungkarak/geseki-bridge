/*
 * Geseki Bridge — the widget dock and the Tools menu.
 *
 *   Tools > Geseki > Geseki Bridge…                        (settings dialog)
 *
 * OBS lets a plugin register a browser dock only by writing an entry into the
 * list it reads at startup (`[BasicWindow] ExtraBrowserDocks` in the user
 * config); its CEF widget is not exported, so a plugin cannot build a browser
 * panel itself. This plugin therefore keeps that entry present: the dock exists
 * on every start without the user having to add it by hand, and OBS's own Docks
 * menu (View > Docks) lists it alongside every other dock. There is no
 * plugin-owned show/hide menu — OBS already provides one.
 *
 * The plugin still owns the dock's initial visibility, because OBS creates a
 * new dock visible and that is what covered the scene:
 *
 *  - `[GesekiBridge] DockVisible` holds the user's choice and defaults to
 *    false, so a fresh install starts with the dock hidden.
 *  - It is applied on the next event-loop turn, not inside the
 *    FINISHED_LOADING callback: OBS is still finishing its own window and dock
 *    setup at that point, and a setVisible() from there does not stick.
 *  - Every later change — OBS's own Docks menu, the dock's X button — is
 *    written back through visibilityChanged, so the choice survives a restart.
 */
#include "widget-docks.hpp"

#include <obs-module.h>
#include <obs-frontend-api.h>
#include <util/config-file.h>

#include <string>

#include "json-util.hpp"
#include "plugin-support.hpp"

#ifdef GESEKI_HAS_QT
#include <QAction>
#include <QDockWidget>
#include <QMainWindow>
#include <QMenu>
#include <QTimer>

#include "settings-dialog.hpp"
#endif

namespace geseki::docks {

namespace {

// Title of the dock. OBS derives its object name from this as title + "_extraBrowser".
constexpr const char *kDockTitle = "Dynamic Island Alert";
constexpr const char *kDockUrl = "https://sekisungkarak.web.id/dynamic-island-alert/dashboard/";

// Fixed id, so repeated runs reuse one identity instead of piling up docks.
constexpr const char *kDockUuid = "9f2c7a41b6e54d0f8a3c1d5e7b904f26";

// Where the user's show/hide choice is stored. Absent means hidden.
constexpr const char *kSection = "GesekiBridge";
constexpr const char *kVisibleKey = "DockVisible";

json::Value MakeString(const std::string &text)
{
	json::Value v;
	v.type = json::Value::Type::String;
	v.text = text;
	return v;
}

// True when `list` already holds an object whose "title" is kDockTitle.
bool ListHasDock(const json::Value &list)
{
	if (list.type != json::Value::Type::Array)
		return false;

	for (const json::Value &item : list.items) {
		const json::Value *title = item.find("title");
		if (title && title->as_string() == kDockTitle)
			return true;
	}
	return false;
}

// Reads ExtraBrowserDocks. Returns false only when the value is present but
// malformed — the caller must then leave the user's data alone.
bool ReadDockList(config_t *config, json::Value &out)
{
	const char *raw = config_get_string(config, "BasicWindow", "ExtraBrowserDocks");
	if (!raw || !*raw)
		return true; // absent: treat as an empty list

	std::string error;
	if (!json::Value::Parse(raw, out, &error)) {
		obs_log(LOG_WARNING, "ExtraBrowserDocks is not valid JSON (%s); not touching it",
			error.c_str());
		return false;
	}
	return true;
}

// Adds the dock entry when it is missing, leaving existing entries untouched.
void EnsureEntry()
{
	config_t *config = obs_frontend_get_user_config();
	if (!config) {
		obs_log(LOG_WARNING, "no user config; cannot register the widget dock");
		return;
	}

	json::Value list;
	if (!ReadDockList(config, list))
		return;

	if (ListHasDock(list))
		return;

	json::Value entry;
	entry.type = json::Value::Type::Object;
	entry.members.push_back(std::make_pair(std::string("title"), MakeString(kDockTitle)));
	entry.members.push_back(std::make_pair(std::string("url"), MakeString(kDockUrl)));
	entry.members.push_back(std::make_pair(std::string("uuid"), MakeString(kDockUuid)));
	list.items.push_back(entry);

	const std::string serialized = json::Serialize(list);
	config_set_string(config, "BasicWindow", "ExtraBrowserDocks", serialized.c_str());
	config_save(config);

	obs_log(LOG_INFO, "registered widget dock '%s'", kDockTitle);
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

QDockWidget *g_dock = nullptr;

// The dock OBS created for us, or nullptr when there is none.
QDockWidget *FindDock()
{
	auto *window = static_cast<QMainWindow *>(obs_frontend_get_main_window());
	if (!window)
		return nullptr;

	const QString object_name = QString(kDockTitle) + "_extraBrowser";
	return window->findChild<QDockWidget *>(object_name);
}

void OnFrontendEvent(enum obs_frontend_event event, void * /*data*/)
{
	// OBS builds the dock during OBSBasic::OBSInit, before this event fires.
	if (event != OBS_FRONTEND_EVENT_FINISHED_LOADING)
		return;

	g_dock = FindDock();
	if (!g_dock) {
		obs_log(LOG_WARNING, "widget dock '%s' was not created by OBS", kDockTitle);
		return;
	}

	// Deleting the dock from OBS's Custom Browser Docks dialog nulls our
	// pointer, so the visibility callback below cannot touch a deleted dock.
	QObject::connect(g_dock, &QObject::destroyed, g_dock, [] { g_dock = nullptr; });

	// Apply the stored choice once OBS has finished its own startup work, then
	// start tracking changes so the choice survives the next restart.
	const bool want = DockVisiblePref();
	QTimer::singleShot(0, g_dock, [want] {
		if (!g_dock)
			return;

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
	EnsureEntry();

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
