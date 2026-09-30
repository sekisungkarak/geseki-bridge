#pragma once

// Geseki Bridge — the widget dock and the Tools menu.
namespace geseki::docks {

// Registers the "Dynamic Island Alert" dock in the OBS user config if it is not
// already there (so OBS's own Docks menu lists it), and builds:
//
//   Tools > Geseki > Geseki Bridge…                        (settings dialog)
//
// Call once from obs_module_load().
void Setup();

} // namespace geseki::docks
