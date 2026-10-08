/*
 * Geseki Bridge, settings backup engine.
 *
 * A snapshot is a folder under the backup directory:
 *
 *   backup-YYYYMMDD-HHMMSS-<label>/
 *     manifest.json
 *     plugin_config/geseki-bridge/...    the plugin's own settings
 *     obs-browser/Local Storage/...      every widget's localStorage
 *
 * The default location is <plugin_config>\geseki-bridge\backup, beside the
 * plugin's config inside OBS's own config tree. That survives a browser-cache
 * wipe (localStorage lives in obs-browser's CEF profile, which OBS can clear)
 * and travels with a portable OBS install.
 *
 * Restore copies the files back. Files OBS currently holds open, the leveldb
 * Chromium keeps for Local Storage, cannot be replaced while OBS runs; those
 * are counted and reported so the user can close OBS and restore again. Nothing
 * is deleted first, so a failed restore never leaves the config half-wiped.
 */
#include "backup.hpp"

#ifdef _WIN32
#include <windows.h>
#endif

#include <obs-module.h>

#include "bridge-server.hpp"
#include "json-util.hpp"
#include "plugin-support.hpp"

#include <algorithm>
#include <cstdint>
#include <cstdio>
#include <string>
#include <vector>

namespace geseki::backup {

namespace {

std::wstring Utf8ToWide(const std::string &s)
{
	const int n = MultiByteToWideChar(CP_UTF8, 0, s.c_str(), -1, nullptr, 0);
	if (n <= 0)
		return std::wstring();
	std::wstring w(static_cast<size_t>(n), L'\0');
	MultiByteToWideChar(CP_UTF8, 0, s.c_str(), -1, &w[0], n);
	w.resize(static_cast<size_t>(n - 1));
	return w;
}

std::string WideToUtf8(const std::wstring &w)
{
	const int n = WideCharToMultiByte(CP_UTF8, 0, w.c_str(), -1, nullptr, 0, nullptr, nullptr);
	if (n <= 0)
		return std::string();
	std::string s(static_cast<size_t>(n), '\0');
	WideCharToMultiByte(CP_UTF8, 0, w.c_str(), -1, &s[0], n, nullptr, nullptr);
	s.resize(static_cast<size_t>(n - 1));
	return s;
}

std::string JoinPath(const std::string &a, const std::string &b)
{
	if (a.empty())
		return b;
	if (a.back() == '\\' || a.back() == '/')
		return a + b;
	return a + "\\" + b;
}

std::string ParentDir(const std::string &p)
{
	const size_t slash = p.find_last_of("\\/");
	if (slash == std::string::npos)
		return std::string();
	return p.substr(0, slash);
}

std::string AbsolutePath(const std::string &path)
{
	if (path.empty())
		return path;
	const std::wstring w = Utf8ToWide(path);
	const DWORD need = GetFullPathNameW(w.c_str(), 0, nullptr, nullptr);
	if (need == 0)
		return path;
	std::wstring buf(static_cast<size_t>(need), L'\0');
	const DWORD got = GetFullPathNameW(w.c_str(), need, &buf[0], nullptr);
	if (got == 0 || got >= need)
		return path;
	buf.resize(got);
	return WideToUtf8(buf);
}

bool PathExists(const std::string &path)
{
	if (path.empty())
		return false;
	const std::wstring w = Utf8ToWide(path);
	return GetFileAttributesW(w.c_str()) != INVALID_FILE_ATTRIBUTES;
}

bool IsDirectory(const std::string &path)
{
	const std::wstring w = Utf8ToWide(path);
	const DWORD a = GetFileAttributesW(w.c_str());
	return a != INVALID_FILE_ATTRIBUTES && (a & FILE_ATTRIBUTE_DIRECTORY);
}

// Creates every missing component. Components that already exist report
// ERROR_ALREADY_EXISTS, which is not an error here.
void EnsureDirRecursive(const std::string &dir)
{
	if (dir.empty())
		return;
	const std::wstring w = Utf8ToWide(dir);
	for (size_t i = 1; i <= w.size(); ++i) {
		const bool at_end = (i == w.size());
		if (!at_end && w[i] != L'\\' && w[i] != L'/')
			continue;
		const std::wstring part = w.substr(0, i);
		if (part.empty() || part.back() == L':')
			continue; // "E:" is a drive, not a directory to create
		CreateDirectoryW(part.c_str(), nullptr);
	}
}

bool WriteFileUtf8(const std::string &path, const std::string &data)
{
	if (path.empty())
		return false;
	EnsureDirRecursive(ParentDir(path));
	const std::wstring w = Utf8ToWide(path);
	HANDLE h = CreateFileW(w.c_str(), GENERIC_WRITE, 0, nullptr, CREATE_ALWAYS,
			       FILE_ATTRIBUTE_NORMAL, nullptr);
	if (h == INVALID_HANDLE_VALUE)
		return false;
	DWORD written = 0;
	const BOOL ok = WriteFile(h, data.data(), static_cast<DWORD>(data.size()), &written, nullptr);
	CloseHandle(h);
	return ok && written == data.size();
}

bool ReadFileUtf8(const std::string &path, std::string &out)
{
	if (path.empty())
		return false;
	const std::wstring w = Utf8ToWide(path);
	HANDLE h = CreateFileW(w.c_str(), GENERIC_READ, FILE_SHARE_READ, nullptr, OPEN_EXISTING,
			       FILE_ATTRIBUTE_NORMAL, nullptr);
	if (h == INVALID_HANDLE_VALUE)
		return false;
	LARGE_INTEGER sz{};
	if (!GetFileSizeEx(h, &sz) || sz.QuadPart < 0 || sz.QuadPart > (16LL << 20)) {
		CloseHandle(h);
		return false;
	}
	out.resize(static_cast<size_t>(sz.QuadPart));
	DWORD got = 0;
	BOOL ok = TRUE;
	if (!out.empty())
		ok = ReadFile(h, &out[0], static_cast<DWORD>(out.size()), &got, nullptr);
	CloseHandle(h);
	if (!ok)
		return false;
	out.resize(got);
	return true;
}

// True when `path` is `root` itself or lives under it.
bool IsUnder(const std::string &path, const std::string &root)
{
	if (root.empty())
		return false;
	if (path.size() < root.size())
		return false;
	if (path.compare(0, root.size(), root) != 0)
		return false;
	return path.size() == root.size() || path[root.size()] == '\\' || path[root.size()] == '/';
}

struct CopyStats {
	int copied = 0;
	int locked = 0;
};

// Recursively copies `src` into `dst`, skipping anything under `exclude`.
// A file that cannot be read (locked by a running CEF) is counted, not fatal.
void CopyTree(const std::string &src, const std::string &dst, const std::string &exclude,
	      CopyStats &stats)
{
	if (!IsDirectory(src))
		return;
	if (IsUnder(AbsolutePath(src), exclude))
		return;

	EnsureDirRecursive(dst);

	WIN32_FIND_DATAW fd{};
	const std::wstring pattern = Utf8ToWide(JoinPath(src, "*"));
	HANDLE h = FindFirstFileW(pattern.c_str(), &fd);
	if (h == INVALID_HANDLE_VALUE)
		return;

	do {
		const std::wstring name = fd.cFileName;
		if (name == L"." || name == L"..")
			continue;

		const std::string child = JoinPath(src, WideToUtf8(name));
		if (IsUnder(AbsolutePath(child), exclude))
			continue;

		if (fd.dwFileAttributes & FILE_ATTRIBUTE_DIRECTORY) {
			CopyTree(child, JoinPath(dst, WideToUtf8(name)), exclude, stats);
			continue;
		}

		const std::wstring ws = Utf8ToWide(child);
		const std::wstring wd = Utf8ToWide(JoinPath(dst, WideToUtf8(name)));
		if (CopyFileW(ws.c_str(), wd.c_str(), FALSE))
			++stats.copied;
		else
			++stats.locked; // in use, or unreadable
	} while (FindNextFileW(h, &fd));
	FindClose(h);
}

void DeleteTree(const std::string &dir)
{
	if (!IsDirectory(dir))
		return;

	WIN32_FIND_DATAW fd{};
	const std::wstring pattern = Utf8ToWide(JoinPath(dir, "*"));
	HANDLE h = FindFirstFileW(pattern.c_str(), &fd);
	if (h != INVALID_HANDLE_VALUE) {
		do {
			const std::wstring name = fd.cFileName;
			if (name == L"." || name == L"..")
				continue;
			const std::string child = JoinPath(dir, WideToUtf8(name));
			if (fd.dwFileAttributes & FILE_ATTRIBUTE_DIRECTORY)
				DeleteTree(child);
			else
				DeleteFileW(Utf8ToWide(child).c_str());
		} while (FindNextFileW(h, &fd));
		FindClose(h);
	}

	const std::wstring w = Utf8ToWide(dir);
	RemoveDirectoryW(w.c_str());
}

long long DirSize(const std::string &dir)
{
	if (!IsDirectory(dir))
		return 0;

	long long total = 0;
	WIN32_FIND_DATAW fd{};
	const std::wstring pattern = Utf8ToWide(JoinPath(dir, "*"));
	HANDLE h = FindFirstFileW(pattern.c_str(), &fd);
	if (h == INVALID_HANDLE_VALUE)
		return 0;
	do {
		const std::wstring name = fd.cFileName;
		if (name == L"." || name == L"..")
			continue;
		const std::string child = JoinPath(dir, WideToUtf8(name));
		if (fd.dwFileAttributes & FILE_ATTRIBUTE_DIRECTORY)
			total += DirSize(child);
		else
			total += (static_cast<long long>(fd.nFileSizeHigh) << 32) | fd.nFileSizeLow;
	} while (FindNextFileW(h, &fd));
	FindClose(h);
	return total;
}

// Snapshot names are used as path components, so keep them to one plain name.
bool IsSafeName(const std::string &name)
{
	if (name.rfind("backup-", 0) != 0)
		return false;
	if (name.size() > 128)
		return false;
	for (char c : name) {
		const bool ok = (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') ||
				(c >= '0' && c <= '9') || c == '-' || c == '_' || c == '.';
		if (!ok)
			return false;
	}
	return true;
}

// OBS in portable mode reports the module config path relative to the working
// directory (..\..\config\obs-studio\...); every path we hand to the UI or to
// ShellExecute must be absolute.
std::string ModuleDir() { return AbsolutePath(geseki::bridge::ModuleConfigDir()); }

std::string SettingsPath() { return JoinPath(ModuleDir(), "backup.json"); }

// The obs-browser profile lives beside the plugin's own config, under the same
// plugin_config root.
std::string ObsBrowserStorageDir()
{
	return JoinPath(JoinPath(ParentDir(ModuleDir()), "obs-browser"), "Local Storage");
}

} // namespace

Settings GetSettings()
{
	Settings s;
	std::string raw;
	if (!ReadFileUtf8(SettingsPath(), raw))
		return s;

	geseki::json::Value v;
	if (!geseki::json::Value::Parse(raw, v, nullptr) || !v.is_object())
		return s;
	if (const auto *e = v.find("auto"); e && e->is_bool())
		s.auto_enabled = e->as_bool(true);
	if (const auto *k = v.find("keep"); k && k->is_number())
		s.keep = static_cast<int>(k->as_int(5));
	if (const auto *d = v.find("dir"); d && d->is_string())
		s.dir = d->as_string();
	if (s.keep < 1)
		s.keep = 1;
	if (s.keep > 100)
		s.keep = 100;
	return s;
}

void SaveSettings(const Settings &s)
{
	Settings norm = s;
	if (norm.keep < 1)
		norm.keep = 1;
	if (norm.keep > 100)
		norm.keep = 100;
	const std::string j = "{\"auto\":" + std::string(norm.auto_enabled ? "true" : "false") +
			      ",\"keep\":" + std::to_string(norm.keep) + ",\"dir\":\"" +
			      geseki::json::Escape(norm.dir) + "\"}";
	WriteFileUtf8(SettingsPath(), j);
}

std::string DefaultDir()
{
	return JoinPath(ModuleDir(), "backup");
}

std::string EffectiveDir()
{
	const Settings s = GetSettings();
	return s.dir.empty() ? DefaultDir() : AbsolutePath(s.dir);
}

std::vector<Entry> List()
{
	std::vector<Entry> out;
	const std::string dir = EffectiveDir();
	if (!IsDirectory(dir))
		return out;

	WIN32_FIND_DATAW fd{};
	const std::wstring pattern = Utf8ToWide(JoinPath(dir, "backup-*"));
	HANDLE h = FindFirstFileW(pattern.c_str(), &fd);
	if (h == INVALID_HANDLE_VALUE)
		return out;
	do {
		if (!(fd.dwFileAttributes & FILE_ATTRIBUTE_DIRECTORY))
			continue;
		const std::string name = WideToUtf8(fd.cFileName);
		if (!IsSafeName(name))
			continue;
		Entry e;
		e.name = name;
		e.size = DirSize(JoinPath(dir, name));
		e.mtime = (static_cast<long long>(fd.ftLastWriteTime.dwHighDateTime) << 32) |
			  fd.ftLastWriteTime.dwLowDateTime;
		out.push_back(e);
	} while (FindNextFileW(h, &fd));
	FindClose(h);

	std::sort(out.begin(), out.end(),
		  [](const Entry &a, const Entry &b) { return a.name > b.name; });
	return out;
}

std::string CreateNow(const std::string &label, std::string &error)
{
	error.clear();

	std::string safe;
	for (char c : label) {
		const bool ok = (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') ||
				(c >= '0' && c <= '9') || c == '-' || c == '_';
		safe += ok ? c : '-';
	}
	if (safe.empty())
		safe = "manual";
	if (safe.size() > 24)
		safe.resize(24);

	SYSTEMTIME st{};
	GetLocalTime(&st);
	char stamp[32];
	snprintf(stamp, sizeof(stamp), "%04d%02d%02d-%02d%02d%02d", st.wYear, st.wMonth, st.wDay,
		 st.wHour, st.wMinute, st.wSecond);
	const std::string name = std::string("backup-") + stamp + "-" + safe;

	const std::string dir = EffectiveDir();
	const std::string dest = JoinPath(dir, name);
	EnsureDirRecursive(dest);
	if (!IsDirectory(dest)) {
		error = "could not create the backup folder";
		return std::string();
	}

	CopyStats stats;
	// The plugin's own config; skip the backup folder itself so snapshots do
	// not nest.
	CopyTree(ModuleDir(), JoinPath(dest, "plugin_config\\geseki-bridge"), dir, stats);
	// Every widget's localStorage.
	CopyTree(ObsBrowserStorageDir(), JoinPath(dest, "obs-browser\\Local Storage"),
		 std::string(), stats);

	char created[32];
	snprintf(created, sizeof(created), "%04d-%02d-%02d %02d:%02d:%02d", st.wYear, st.wMonth,
		 st.wDay, st.wHour, st.wMinute, st.wSecond);
	const std::string manifest =
		std::string("{\"created\":\"") + created + "\",\"label\":\"" +
		geseki::json::Escape(safe) + "\",\"bridge\":\"" GESEKI_BRIDGE_VERSION "\",\"files\":" +
		std::to_string(stats.copied) + "}";
	WriteFileUtf8(JoinPath(dest, "manifest.json"), manifest);

	PruneTo(GetSettings().keep);
	return name;
}

bool Restore(const std::string &name, std::string &report, std::string &error)
{
	error.clear();
	report.clear();

	if (!IsSafeName(name)) {
		error = "invalid backup name";
		return false;
	}
	const std::string src = JoinPath(EffectiveDir(), name);
	if (!IsDirectory(src)) {
		error = "that backup no longer exists";
		return false;
	}

	CopyStats stats;
	CopyTree(JoinPath(src, "plugin_config\\geseki-bridge"), ModuleDir(), std::string(), stats);
	CopyTree(JoinPath(src, "obs-browser\\Local Storage"), ObsBrowserStorageDir(),
		 std::string(), stats);

	report = std::to_string(stats.copied) + " file(s) restored";
	if (stats.locked > 0)
		report += ", " + std::to_string(stats.locked) +
			  " could not be written because OBS is using them";
	return true;
}

void PruneTo(int keep)
{
	if (keep < 1)
		keep = 1;
	const std::string dir = EffectiveDir();
	const std::vector<Entry> files = List();
	for (size_t i = static_cast<size_t>(keep); i < files.size(); ++i)
		DeleteTree(JoinPath(dir, files[i].name));
}

void MaybeAutoBackup()
{
	const Settings s = GetSettings();
	if (!s.auto_enabled)
		return;

	SYSTEMTIME st{};
	GetLocalTime(&st);
	char today[16];
	snprintf(today, sizeof(today), "%04d%02d%02d", st.wYear, st.wMonth, st.wDay);

	// Names sort chronologically: "backup-YYYYMMDD-HHMMSS-label".
	for (const Entry &e : List()) {
		if (e.name.size() >= 15 && e.name.compare(7, 8, today) == 0) {
			obs_log(LOG_INFO, "geseki-bridge: backup for today already exists (%s)",
				e.name.c_str());
			return;
		}
	}

	std::string error;
	const std::string name = CreateNow("auto", error);
	if (name.empty())
		obs_log(LOG_WARNING, "geseki-bridge: automatic backup failed: %s", error.c_str());
	else
		obs_log(LOG_INFO, "geseki-bridge: automatic backup written (%s)", name.c_str());
}

} // namespace geseki::backup
