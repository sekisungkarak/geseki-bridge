#pragma once

// Native (Qt6) settings dialog for the Geseki Bridge plugin. Shown from the
// OBS Tools menu so the TikTok credentials are edited inside OBS rather than
// in a browser. `parent` is the OBS main window as a QWidget*; it is passed as
// void* so non-Qt translation units can include this header.
namespace geseki::ui {

void ShowSettingsDialog(void *parent);

} // namespace geseki::ui
