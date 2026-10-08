#pragma once

// Geseki Bridge, the widget docks and the Tools menu.
namespace geseki::docks {

// Builds the Tools menu and, on OBS_FRONTEND_EVENT_FINISHED_LOADING, the
// plugin-owned dashboard docks (see kDocks in widget-docks.cpp):
//
//   Tools > Geseki > Geseki Bridge…                        (settings dialog)
//
// Each dock is added with obs_frontend_add_dock_by_id(), so it belongs to the
// plugin: OBS lists it in its own Docks menu, but it is not one of the user's
// Custom Browser Docks and therefore cannot be deleted from the UI. Each has
// its own visibility key, so they show and hide independently.
//
// Call once from obs_module_load().
void Setup();

} // namespace geseki::docks
