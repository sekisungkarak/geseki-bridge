#pragma once

#include <string>
#include <vector>

// Settings backup engine for the Geseki Bridge plugin (no Qt).
//
// A snapshot is a folder under the backup directory holding a copy of the
// plugin's own config and of obs-browser's Local Storage, where every widget's
// settings live. Folders, not a zip: the plugin ships no zip writer, and a plain
// folder can be opened, inspected and copied by hand.
namespace geseki::backup {

struct Settings {
	// Write a snapshot when OBS starts, at most once a day.
	bool auto_enabled = true;
	// Snapshots to keep; older ones are pruned after each new snapshot.
	int keep = 5;
	// Empty = DefaultDir().
	std::string dir;
};

struct Entry {
	std::string name;
	long long size = 0;
	long long mtime = 0;
};

Settings GetSettings();
void SaveSettings(const Settings &s);

// <plugin_config>\geseki-bridge\backup, absolute.
std::string DefaultDir();
std::string EffectiveDir();

// Newest first.
std::vector<Entry> List();

// Writes a snapshot. Returns its folder name, or "" with `error` filled.
std::string CreateNow(const std::string &label, std::string &error);

// Copies a snapshot back over the live config. `report` receives a readable
// summary; files OBS holds open are counted so the user knows to close OBS and
// retry. Returns false with `error` filled on a hard failure.
bool Restore(const std::string &name, std::string &report, std::string &error);

// Deletes all but the newest `keep` snapshots.
void PruneTo(int keep);

// Called once at plugin load: writes a snapshot when auto-backup is on and none
// exists for today. Safe there because obs-browser has not opened its Local
// Storage yet, so no file is locked.
void MaybeAutoBackup();

} // namespace geseki::backup
