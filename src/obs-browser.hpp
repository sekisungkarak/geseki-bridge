#pragma once

// The obs-browser panel factory, as seen from another plugin.
//
// OBS lets a plugin add a dock of its own with obs_frontend_add_dock_by_id(),
// but the widget it shows has to come from obs-browser's CEF factory. That
// interface (obs-browser's panel/browser-panel.hpp) is not part of the plugin
// SDK, so the small slice this plugin needs is declared here.
//
// Only the virtual slots up to create_widget() are declared: a vtable prefix is
// fixed by declaration order, so calling create_widget() reaches the right
// entry. QCefWidget is declared below with the same slot order as obs-browser's,
// so the pointer it returns can also close the browser (see widget-docks.cpp).

#include <obs.h>
#include <util/platform.h>

#include <QWidget>

#include <string>

// The CEF panel widget. Only the slots up to closeBrowser() are declared, and
// in obs-browser's order, so that call reaches the right vtable entry.
class QCefWidget : public QWidget {
public:
	virtual void setURL(const std::string &url) = 0;
	virtual void setStartupScript(const std::string &script) = 0;
	virtual void allowAllPopups(bool allow) = 0;
	virtual void closeBrowser() = 0;
	virtual void reloadPage() = 0;
	virtual bool zoomPage(int direction) = 0;
	virtual void executeJavaScript(const std::string &script) = 0;
};

struct QCef {
	virtual ~QCef() {}
	virtual bool init_browser() = 0;
	virtual bool initialized() = 0;
	virtual bool wait_for_browser_init() = 0;
	virtual QCefWidget *create_widget(QWidget *parent, const std::string &url,
					  void *cookie_manager = nullptr) = 0;
};

namespace geseki::browser {

// The CEF factory, or nullptr when obs-browser is not installed. OBS's own
// frontend reaches the factory the same way: find the module by name, then
// resolve its exported symbol.
inline QCef *Panel()
{
	obs_module_t *module = obs_get_module("obs-browser");
	if (!module)
		return nullptr;

	void *lib = obs_get_module_lib(module);
	if (!lib)
		return nullptr;

	using CreateFn = QCef *(*)(void);
	auto create = reinterpret_cast<CreateFn>(os_dlsym(lib, "obs_browser_create_qcef"));
	return create ? create() : nullptr;
}

} // namespace geseki::browser
