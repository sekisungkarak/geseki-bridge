#pragma once

// Native (Qt6) backup dialog for the Geseki Bridge plugin. Shown from the OBS
// Tools menu, next to the settings dialog. `parent` is the OBS main window as a
// QWidget*; it is passed as void* so non-Qt translation units can include this
// header.
namespace geseki::ui {

void ShowBackupDialog(void *parent);

} // namespace geseki::ui
