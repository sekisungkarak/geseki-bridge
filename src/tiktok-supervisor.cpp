/*
 * TikTok sidecar supervisor (Windows).
 *
 * The sidecar is a console program, so it is started with pipes and
 * CREATE_NO_WINDOW: without that flag a black console window would flash on
 * every OBS start.
 *
 * stdin/stdout carry protocol frames; stderr is drained to the OBS log so a
 * crashing sidecar is diagnosable from the OBS log window.
 */
#include "tiktok-supervisor.hpp"
#include "plugin-support.hpp"

#include <obs-module.h>

#ifdef _WIN32

#include <windows.h>

#include <atomic>
#include <chrono>
#include <mutex>
#include <thread>

namespace {

std::mutex g_mu;
HANDLE g_proc = nullptr;      // child process
HANDLE g_stdin = nullptr;     // write end -> child stdin
HANDLE g_stdout_read = nullptr; // read end <- child stdout
HANDLE g_stderr_read = nullptr; // read end <- child stderr
std::thread g_reader;
std::thread g_err_reader;
std::atomic<bool> g_running{false};
geseki::tiktok::MessageHandler g_handler;

std::wstring ExePath()
{
	// The sidecar ships beside the plugin DLL, so resolve relative to our own
	// module rather than the OBS working directory.
	HMODULE self = nullptr;
	GetModuleHandleExW(GET_MODULE_HANDLE_EX_FLAG_FROM_ADDRESS |
				   GET_MODULE_HANDLE_EX_FLAG_UNCHANGED_REFCOUNT,
			   reinterpret_cast<LPCWSTR>(&ExePath), &self);

	wchar_t path[MAX_PATH]{};
	GetModuleFileNameW(self, path, MAX_PATH);

	std::wstring dir(path);
	const size_t slash = dir.find_last_of(L"\\/");
	if (slash != std::wstring::npos)
		dir.resize(slash);
	return dir + L"\\geseki-bridge-tiktok.exe";
}

// Splits the child's stdout into newline-delimited JSON frames.
void ReaderLoop()
{
	std::string buffer;
	char chunk[4096];
	DWORD read = 0;

	while (g_running.load()) {
		if (!ReadFile(g_stdout_read, chunk, sizeof(chunk), &read, nullptr) || read == 0)
			break;

		buffer.append(chunk, read);

		size_t pos;
		while ((pos = buffer.find('\n')) != std::string::npos) {
			std::string line = buffer.substr(0, pos);
			buffer.erase(0, pos + 1);
			if (!line.empty() && line.back() == '\r')
				line.pop_back();
			if (!line.empty() && g_handler)
				g_handler(line);
		}
	}
	g_running.store(false);
}

// Keeps the stderr pipe drained. If nobody reads it, a chatty child blocks
// once the pipe buffer fills and appears to hang.
void ErrReaderLoop()
{
	char chunk[1024];
	DWORD read = 0;
	while (ReadFile(g_stderr_read, chunk, sizeof(chunk) - 1, &read, nullptr) && read > 0) {
		chunk[read] = '\0';
		obs_log(LOG_DEBUG, "geseki-bridge[tiktok]: %s", chunk);
	}
}

void CloseIfValid(HANDLE &h)
{
	if (h) {
		CloseHandle(h);
		h = nullptr;
	}
}

} // namespace

namespace geseki::tiktok {

bool Start(const std::string &username, const std::string &signerUrl,
           const std::string &apiKey, MessageHandler handler)
{
	Stop();

	const std::wstring exe = ExePath();
	if (GetFileAttributesW(exe.c_str()) == INVALID_FILE_ATTRIBUTES) {
		obs_log(LOG_WARNING, "geseki-bridge: TikTok sidecar not found at %ls", exe.c_str());
		return false;
	}

	SECURITY_ATTRIBUTES sa{};
	sa.nLength = sizeof(sa);
	sa.bInheritHandle = TRUE;

	HANDLE in_read = nullptr, in_write = nullptr;
	HANDLE out_read = nullptr, out_write = nullptr;
	HANDLE err_read = nullptr, err_write = nullptr;

	if (!CreatePipe(&in_read, &in_write, &sa, 0) ||
	    !CreatePipe(&out_read, &out_write, &sa, 0) ||
	    !CreatePipe(&err_read, &err_write, &sa, 0)) {
		obs_log(LOG_ERROR, "geseki-bridge: CreatePipe failed (err %lu)", GetLastError());
		return false;
	}

	// Only the child's ends may be inherited; leaking our ends into the child
	// would keep the pipes open after we close them.
	SetHandleInformation(in_write, HANDLE_FLAG_INHERIT, 0);
	SetHandleInformation(out_read, HANDLE_FLAG_INHERIT, 0);
	SetHandleInformation(err_read, HANDLE_FLAG_INHERIT, 0);

	STARTUPINFOW si{};
	si.cb = sizeof(si);
	si.dwFlags = STARTF_USESTDHANDLES;
	si.hStdInput = in_read;
	si.hStdOutput = out_write;
	si.hStdError = err_write;

	PROCESS_INFORMATION pi{};
	std::wstring cmd = L"\"" + exe + L"\"";
	std::vector<wchar_t> cmd_buf(cmd.begin(), cmd.end());
	cmd_buf.push_back(L'\0');

	const BOOL ok = CreateProcessW(nullptr, cmd_buf.data(), nullptr, nullptr, TRUE,
				       CREATE_NO_WINDOW, nullptr, nullptr, &si, &pi);

	// Our copies of the child's ends are no longer needed either way.
	CloseIfValid(in_read);
	CloseIfValid(out_write);
	CloseIfValid(err_write);

	if (!ok) {
		obs_log(LOG_ERROR, "geseki-bridge: failed to start TikTok sidecar (err %lu)", GetLastError());
		CloseIfValid(in_write);
		CloseIfValid(out_read);
		CloseIfValid(err_read);
		return false;
	}

	CloseHandle(pi.hThread);

	{
		std::lock_guard<std::mutex> lk(g_mu);
		g_proc = pi.hProcess;
		g_stdin = in_write;
		g_stdout_read = out_read;
		g_stderr_read = err_read;
		g_handler = std::move(handler);
	}

	g_running.store(true);
	g_reader = std::thread(ReaderLoop);
	g_err_reader = std::thread(ErrReaderLoop);

	if (!username.empty()) {
		std::string json = "{\"cmd\":\"connect\",\"username\":\"" + username + "\"";
		// Omitted in the alternative connection mode, which is how the
		// sidecar tells the two apart.
		if (!signerUrl.empty())
			json += ",\"signerUrl\":\"" + signerUrl + "\"";
		if (!apiKey.empty())
			json += ",\"apiKey\":\"" + apiKey + "\"";
		json += "}";
		Send(json);
	}
	return true;
}

void Stop()
{
	{
		std::lock_guard<std::mutex> lk(g_mu);
		if (!g_proc)
			return;
	}

	Send("{\"cmd\":\"quit\"}");

	// Give it a moment to exit cleanly, then force it: an orphan would keep the
	// port and the TikTok socket alive after OBS closes.
	std::this_thread::sleep_for(std::chrono::milliseconds(400));

	HANDLE proc = nullptr;
	{
		std::lock_guard<std::mutex> lk(g_mu);
		proc = g_proc;
	}
	if (proc && WaitForSingleObject(proc, 800) == WAIT_TIMEOUT)
		TerminateProcess(proc, 0);

	g_running.store(false);

	// Unblock the reader threads by closing our ends of the pipes.
	{
		std::lock_guard<std::mutex> lk(g_mu);
		CloseIfValid(g_stdin);
		CloseIfValid(g_stdout_read);
		CloseIfValid(g_stderr_read);
	}

	if (g_reader.joinable())
		g_reader.join();
	if (g_err_reader.joinable())
		g_err_reader.join();

	std::lock_guard<std::mutex> lk(g_mu);
	CloseIfValid(g_proc);
	g_handler = nullptr;
}

void Send(const std::string &json_line)
{
	std::lock_guard<std::mutex> lk(g_mu);
	if (!g_stdin)
		return;
	DWORD written = 0;
	std::string line = json_line + "\n";
	WriteFile(g_stdin, line.data(), static_cast<DWORD>(line.size()), &written, nullptr);
}

bool Running()
{
	return g_running.load();
}

} // namespace geseki::tiktok

#else // !_WIN32

namespace geseki::tiktok {

bool Start(const std::string &, const std::string &, const std::string &, MessageHandler)
{
	return false;
}
void Stop() {}
void Send(const std::string &) {}
bool Running() { return false; }

} // namespace geseki::tiktok

#endif
