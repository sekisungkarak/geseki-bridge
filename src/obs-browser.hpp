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
// entry. It returns QWidget* rather than obs-browser's QCefWidget* — the two
// are the same pointer (QCefWidget derives from QWidget with no virtual base),
// and the caller only ever needs a QWidget*.

#include <obs.h>
#include <util/platform.h>

#include <QWidget>

#include <string>

struct QCef {
	virtual ~QCef() {}
	virtual bool init_browser() = 0;
	virtual bool initialized() = 0;
	virtual bool wait_for_browser_init() = 0;
	virtual QWidget *create_widget(QWidget *parent, const std::string &url,
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
