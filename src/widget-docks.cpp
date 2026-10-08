/*
 * Geseki Bridge, the widget dock and the Tools menu.
 *
 *   Tools > Geseki > Geseki Bridge…                        (settings dialog)
 *
 * The dock is a CEF panel built by obs-browser and handed to OBS with
 * obs_frontend_add_dock_by_id(). That is the difference that matters here: a
 * dock registered this way belongs to the plugin, so OBS lists it in its own
 * Docks menu but never in the user's "Custom Browser Docks" list, it cannot be
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
 *  - Every later change, OBS's Docks menu, the dock's X button, is written
 *    back through visibilityChanged, so the choice survives a restart.
 *
 * Hiding the dock also closes its CEF browser. OBS only hides the widget, and a
 * browser panel's native window comes back blank (white) when it is shown
 * again; obs-browser only builds the browser in showEvent when there is none,
 * so it never recovers on its own. Closing it here lets that showEvent build a
 * clean one, which is what OBS does for its own browser docks on close.
 */
#include "widget-docks.hpp"

#include <obs-module.h>
#include <obs-frontend-api.h>
#include <util/config-file.h>

#include <string>
#include <vector>

#include "json-util.hpp"
#include "obs-browser.hpp"
#include "plugin-support.hpp"

#ifdef GESEKI_HAS_QT
#include <QAction>
#include <QDockWidget>
#include <QEvent>
#include <QMainWindow>
#include <QMenu>
#include <QPointer>
#include <QTimer>

#include "settings-dialog.hpp"
#include "backup-dialog.hpp"
#endif

namespace geseki::docks {

namespace {

// One plugin-owned dock. The id is fixed so repeated runs reuse one identity,
// and OBS rejects a second dock that claims the same object name.
struct DockDef {
	const char *id;
	const char *title;
	const char *url;
	// Where this dock's show/hide choice is stored. Absent means hidden. The
	// keys are separate so one dock can be shown without the other.
	const char *visibleKey;
	// OBS creates a plugin dock with no size of its own and a CEF panel's
	// sizeHint is tiny, so without this the dock opens too small to use.
	int width;
	int height;
};

// Both dashboards are the same shared settings page, so they open at the same
// size; what differs is the widget each one configures.
const DockDef kDocks[] = {
	{
		"gesekiDynamicIslandAlertDock",
		"Dynamic Island Alert",
		"https://sekisungkarak.web.id/dynamic-island-alert/dashboard/",
		"DockVisible",
		550,
		900,
	},
	{
		"gesekiLiveQaDock",
		"Live Q&A",
		"https://sekisungkarak.web.id/live-qa/dashboard/",
		"LiveQaDockVisible",
		550,
		900,
	},
};

constexpr const char *kSection = "GesekiBridge";

// Drops every entry whose "title" matches one of our docks, so they no longer
// appear in the user's Custom Browser Docks list (where they could be deleted).
// Leaves the rest of the user's entries untouched.
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
		bool isOurs = false;
		if (title) {
			for (const DockDef &def : kDocks) {
				if (title->as_string() == def.title) {
					isOurs = true;
					break;
				}
			}
		}
		if (isOurs) {
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

	obs_log(LOG_INFO, "removed legacy Custom Browser Dock entries");
}

// The user's stored choice for one dock; hidden unless they turned it on.
bool DockVisiblePref(const DockDef &def)
{
	config_t *config = obs_frontend_get_user_config();
	return config && config_get_bool(config, kSection, def.visibleKey);
}

void SaveDockVisiblePref(const DockDef &def, bool visible)
{
	config_t *config = obs_frontend_get_user_config();
	if (!config)
		return;

	config_set_bool(config, kSection, def.visibleKey, visible);
	config_save(config);
}

#ifdef GESEKI_HAS_QT

// One entry per dock in kDocks, in the same order.
std::vector<QPointer<QDockWidget>> g_docks;

// Set once OBS starts shutting down: hiding a dock at that point must not run
// closeBrowser()'s nested event loop.
bool g_shutting_down = false;

// A browser panel keeps its native window while its dock is hidden, and comes
// back blank (white) when the dock is shown again: obs-browser only builds the
// browser in showEvent when there is none, so it never recovers on its own.
// Closing the browser as the dock is hidden lets that showEvent build a clean
// one, the same thing OBS does for its own browser docks when they close.
//
// The hide is caught on the dock, not through visibilityChanged: OBS hides a
// dock from its Docks menu with that dock's signals blocked, and an event is
// not blocked. Closing is deferred one turn because closeBrowser() runs a
// nested event loop and the hide itself must finish first.
class DockHideCloser : public QObject {
public:
	DockHideCloser(QCefWidget *browser_, QObject *parent) : QObject(parent), browser(browser_) {}

protected:
	bool eventFilter(QObject * /*watched*/, QEvent *event) override
	{
		if (event->type() != QEvent::Hide || !browser || g_shutting_down)
			return false;

		QPointer<QCefWidget> panel = browser;
		auto *dock = qobject_cast<QDockWidget *>(parent());
		QTimer::singleShot(0, this, [panel, dock] {
			if (panel && dock && !dock->isVisible() && !g_shutting_down)
				panel->closeBrowser();
		});
		return false;
	}

private:
	QPointer<QCefWidget> browser;
};

void CreateDock(const DockDef &def, QCef *cef, QMainWindow *window)
{
	QCefWidget *browser = cef->create_widget(nullptr, def.url);
	if (!browser) {
		obs_log(LOG_WARNING, "could not create the '%s' browser panel", def.title);
		g_docks.emplace_back(nullptr);
		return;
	}

	// A plugin-owned dock: OBS gives it a Docks menu entry and a close button,
	// but it is not one of the user's Custom Browser Docks, so the Custom
	// Browser Docks dialog cannot delete it.
	if (!obs_frontend_add_dock_by_id(def.id, def.title, browser)) {
		obs_log(LOG_WARNING, "could not register the '%s' dock", def.title);
		g_docks.emplace_back(nullptr);
		return;
	}

	QPointer<QDockWidget> dock =
		window ? window->findChild<QDockWidget *>(QString::fromUtf8(def.id)) : nullptr;
	if (!dock) {
		obs_log(LOG_WARNING, "dock '%s' was not created by OBS", def.title);
		g_docks.emplace_back(nullptr);
		return;
	}

	g_docks.push_back(dock);
	dock->installEventFilter(new DockHideCloser(browser, dock));

	// Apply the stored choice once OBS has finished its own startup work, then
	// start tracking changes so the choice survives the next restart.
	const bool want = DockVisiblePref(def);
	QTimer::singleShot(0, dock, [dock, def, want] {
		if (!dock)
			return;

		// OBS gives a plugin dock no size of its own and a CEF panel reports
		// a tiny sizeHint, so it would otherwise open too small to use.
		dock->resize(def.width, def.height);
		dock->setVisible(want);

		QObject::connect(dock, &QDockWidget::visibilityChanged, dock,
				 [def](bool visible) { SaveDockVisiblePref(def, visible); });

		obs_log(LOG_INFO, "dock '%s' visibility applied: %s", def.title,
			want ? "shown" : "hidden");
	});
}

void OnFrontendEvent(enum obs_frontend_event event, void * /*data*/)
{
	// OBS hides its docks while it tears the window down. Remember that, so a
	// hide at that point does not run closeBrowser()'s nested event loop.
	if (event == OBS_FRONTEND_EVENT_EXIT || event == OBS_FRONTEND_EVENT_SCRIPTING_SHUTDOWN) {
		g_shutting_down = true;
		return;
	}

	// The frontend exists by now: the main window is up and the browser panel
	// is available, so the docks can be created.
	if (event != OBS_FRONTEND_EVENT_FINISHED_LOADING)
		return;

	if (!g_docks.empty())
		return;

	QCef *cef = geseki::browser::Panel();
	if (!cef) {
		obs_log(LOG_WARNING, "obs-browser is unavailable; the widget docks were not created");
		return;
	}

	auto *window = static_cast<QMainWindow *>(obs_frontend_get_main_window());

	g_docks.reserve(sizeof(kDocks) / sizeof(kDocks[0]));
	for (const DockDef &def : kDocks)
		CreateDock(def, cef, window);
}

void OpenSettings()
{
	geseki::ui::ShowSettingsDialog(obs_frontend_get_main_window());
}

void OpenBackup()
{
	geseki::ui::ShowBackupDialog(obs_frontend_get_main_window());
}

#endif // GESEKI_HAS_QT

} // namespace

void Setup()
{
	// The docks used to be Custom Browser Docks; drop those entries so they stop
	// showing up in the dialog that could delete them.
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

	QAction *backup = root->addAction(obs_module_text("GesekiBridge.BackupItem"));
	QObject::connect(backup, &QAction::triggered, root, [] { OpenBackup(); });

	obs_frontend_add_event_callback(OnFrontendEvent, nullptr);
#endif
}

} // namespace geseki::docks
