# Geseki Bridge — Handoff

State of the scaffold, so a fresh session can pick this up without re-deriving
anything. Written 2026-09-29.

---

## 1. What this is

**Geseki Bridge** = one OBS plugin that replaces three separate local services
the Sekisungkarak widgets used to need:

| Old service | Port | Replaced by |
| --- | --- | --- |
| TikFinity WebSocket | `21213` | `tiktok` events from the bridge |
| IndoFinity WebSocket | `62024` | `tiktok` events from the bridge |
| SMTC Bridge (Python/Flask tray app) | `5000` | `nowplaying` events from the bridge |

The plugin hosts **one loopback WebSocket server** (`ws://127.0.0.1:47800/ws`)
plus a tiny HTTP surface (`/health`, `/now-playing`, `/artwork/<app_id>`,
settings page).

Widgets then need **one** setting (the bridge port) instead of three
(`tikfinityPort`, `indofinityPort`, `smtcBridgePort`).

Repo: `E:/Sekisungkarak/geseki-bridge` — **separate from the site repo**, on
purpose: `sekisungkarak.github.io`'s Pages workflow uploads `path: .` (the whole
repo becomes public) and the plugin has a different toolchain/lifecycle
(CMake + OBS SDK + per-platform release). Do **not** move this into the site
repo.

---

## 2. Locked decisions

1. **TikTok engine = Go sidecar** using
   [`steampoweredtaco/gotiktoklive`](https://github.com/steampoweredtaco/gotiktoklive)
   — MIT, single binary, active.
   Rejected: `zerodytrash/TikTok-Live-Connector` (Node ESM, AGPL-3.0-only,
   needs a ~50 MB Node runtime) and `isaackogan/TikTokLive` (Python, AGPL).
2. **Sidecar ≠ second port.** The plugin spawns the sidecar and talks to it over
   **stdin/stdout, newline-delimited JSON**. Only one port (the WebSocket) is
   ever opened.
3. **SMTC is read natively via WinRT** inside the plugin — no external tray app.
   `GlobalSystemMediaTransportControlsSessionManager`, polled from a worker
   thread.
4. **Settings UI is browser-based** (`ShowSettings()` opens the plugin's own
   HTTP page). Keeps the plugin free of a Qt dependency.
5. Widgets must **ignore unknown `type` values and unknown keys** — additive
   protocol changes are not breaking; only the integer `protocol` field is.

> **Constraint from the user:** the local folder `E:/Sekisungkarak/smtc-bridge`
> is a *modified* copy and must **NOT** be used as a reference. The SMTC data
> shape below is the target contract; upstream reference is
> <https://github.com/nuttylmao/smtc-bridge>.

---

## 3. Files present

```
docs/protocol.md            wire contract, v1 — READ THIS FIRST
src/plugin-main.cpp         module entry: Start()/Stop(), Tools menu item
src/plugin-support.hpp      PLUGIN_NAME / VERSION / GESEKI_BRIDGE_PROTOCOL
src/bridge-server.hpp       Start/Stop/ShowSettings/GetConfig/SaveConfig + Config
src/bridge-server.cpp       the server: WS + HTTP + SMTC poll + TikTok wiring
src/json-util.hpp/.cpp      JSON emitters + a small parser (Value/Parse/Serialize)
src/smtc-winrt.hpp/.cpp     WinRT SMTC reader + transport control
src/tiktok-supervisor.hpp   sidecar interface (Start/Stop/Send/Running)
src/tiktok-supervisor.cpp   Windows CreateProcess supervisor (pipes, no window)
sidecar/tiktok/go.mod       module + gotiktoklive v0.0.4
sidecar/tiktok/main.go      Go sidecar: stdio JSON protocol, event normalisation
```

### What `bridge-server.cpp` does (done)

- One loopback listener (`SO_EXCLUSIVEADDRUSE`, no reuse) at `127.0.0.1:<port>`.
- WebSocket RFC 6455 handshake (hand-rolled SHA-1 + Base64 — verified against the
  RFC vector `dGhlIHNhbXBsZSBub25jZQ==` → `s3pPLMBiTxaQ9kYGzzhZRbK+xOo=`) and a
  minimal frame reader/writer (text, close, ping, pong; 4 MiB frame cap).
- HTTP: `GET /health`, `GET /now-playing` (legacy SMTC-Bridge payload),
  `GET /artwork/<app_id>?v=<n>`, `GET /` (settings page), `POST /config`.
- Client registry + `subscribe` filtering + `broadcast()`.
- Commands: `ping`→`pong`, `subscribe`, `tiktok.connect`, `tiktok.disconnect`,
  `smtc.control`.
- SMTC worker: polls `geseki::smtc::Poll()` ~1×/s, builds the `nowplaying`
  payload, pushes on change (and ≥1×/s while playing). Artwork cached in memory,
  served at `/artwork/...` with a monotonic `?v=` cache-buster.
- TikTok wiring: `tiktok::Start()` with a handler that re-serialises sidecar
  `{"ev":"tiktok"}` frames into the public `{"type":"tiktok"}` shape; a
  maintenance thread restarts the sidecar if it dies (with backoff).
- Config load/save via `obs_module_config_path("config.json")` +
  `obs_data_save_json_safe` (both `obs-module.h` / `obs.h`).

Design choices worth keeping: `color`/`palette` are **not** emitted (the widget
computes its own Vibrant palette); `Thumbnail` is a URL, not base64.

## 4. Build files (done)

- **`CMakeLists.txt`** — `obs-plugintemplate` flow: `ENABLE_FRONTEND_API` ON,
  no Qt. Sources: `plugin-main.cpp`, `bridge-server.cpp`, `json-util.cpp`,
  `smtc-winrt.cpp`, `tiktok-supervisor.cpp`. Links `OBS::libobs`,
  `OBS::obs-frontend-api`, and on Windows `ws2_32 shell32 windowsapp
  runtimeobject`, with `/bigobj` for the WinRT headers. **No C++20 needed** —
  `smtc-winrt.cpp` uses the blocking `IAsyncOperation::get()`, not `co_await`, so
  the C++17 default from `compiler_common.cmake` is enough.
- **`cmake/`** — vendored verbatim from `obs-plugintemplate` (`common/*` +
  `windows/*`), except `cmake/windows/buildspec.cmake` where **qt6 was removed
  from `dependencies_list`** (no Qt here).
- **`CMakePresets.json`** — Windows x64 only (`windows-x64`, `windows-ci-x64`).
- **`buildspec.json`** — name `geseki-bridge`, version `0.2.0`, OBS sources
  `31.1.1`, prebuilt deps `2025-07-11`. Needs `platformConfig.macos.bundleId`
  because `bootstrap.cmake` reads it unconditionally.
- **`data/locale/en-US.ini`** — `GesekiBridge.MenuItem`. Note the path is
  `data/locale/`, **not** `resources/`; the template's `target_install_resources`
  installs `data/` to `<plugin>/data/`, which is where OBS looks.
- **`.github/workflows/release.yml`** — builds the Go sidecar
  (`GOOS=windows GOARCH=amd64`) then the plugin via CMake, copies the sidecar
  next to the DLL, zips `geseki-bridge/`, uploads the artifact and attaches it to
  a GitHub Release on `v*` tags.
- **`README.md`**, **`LICENSE`** (GPL-2.0, same as OBS — required for the
  frontend-api linkage), **`.gitignore`**.

## 5. Still missing (the actual work left)

- **`sidecar/tiktok/go.sum`** — must be generated by `go mod tidy` on a machine
  with Go (or let CI do it; the workflow runs `go mod tidy` first).
- **`git init`** — the folder is not a git repo yet. Do not commit/push until the
  user says so.
- Nothing has been **compiled** yet — there is no local toolchain (see below).

## 6. Build

Toolchain is **not installed on this machine** (no Go, no CMake, no MSVC, no
Windows SDK) — only git. So compile via GitHub Actions, or after the user
installs:

- Go sidecar: `cd sidecar/tiktok && go mod tidy && GOOS=windows GOARCH=amd64 go build -trimpath -ldflags "-s -w" -o ../../bin/geseki-bridge-tiktok.exe .`
- Plugin: `cmake --preset windows-x64 && cmake --build --preset windows-x64 --config RelWithDebInfo --parallel && cmake --install build_x64 --prefix release --config RelWithDebInfo`

The plugin resolves the sidecar **relative to its own DLL**
(`GetModuleFileNameW` on the module handle), so the exe must sit next to
`geseki-bridge.dll` in the OBS plugin dir.

## 7. Upstream API facts (verified, do not re-derive)

- `gotiktoklive` = **Go 1.23**, module `github.com/steampoweredtaco/gotiktoklive`,
  pinned `v0.0.4`. Entry: `NewTikTok(username, opts...)`, methods `TrackUser`,
  `TrackRoom`, `GetUserInfo`. Events are delivered on channels; `types.go`
  holds `ChatEvent`, `GiftEvent`, `LikeEvent`, `RoomEvent`, `UserEvent`,
  `ViewersEvent`, etc.
- Signing goes through the **Euler signer** `https://tiktok.eulerstream.com`.
  `apiKey` is optional; it raises the rate limit. Same caveat for every
  language port.
- SMTC enums: `PlaybackStatus` 0 CLOSED, 1 OPENED, 2 CHANGING, 3 STOPPED,
  4 PLAYING, 5 PAUSED. `PlaybackType` 0 UNKNOWN, 1 MUSIC, 2 VIDEO, 3 IMAGE.
  Timeline values are **milliseconds** (`TimeSpan` ticks / 10000).
- WinRT `DateTime` is 100 ns ticks since 1601-01-01; subtract
  `116444736000000000` to get Unix time.

## 8. Protocol summary

Full detail in `docs/protocol.md`. Server → client: `hello`, `status`,
`tiktok`, `nowplaying`, `pong`. Client → server: `ping`, `subscribe`,
`tiktok.connect`, `tiktok.disconnect`, `smtc.control`.

TikTok `event` values (already normalised for the widget, which reads
`userId` / `uniqueId` / `nickname` / `profilePictureUrl` / `userBadges[]` with
`image`/`name`/`color`): `chat`, `gift`, `follow`, `share`, `subscribe`,
`like`, `roomUser`, `join`.

## 9. Widget-side follow-up (separate, in the site repo)

**SMTC is already a drop-in.** The widget builds
`http://127.0.0.1:${smtcBridgePort}/now-playing` in
`dynamic-island-alert/script.js` — exactly the path the bridge now serves with
the same schema. Pointing `smtcBridgePort` at the bridge port (47800) is enough;
no widget code change for now-playing.

What is left is the **TikTok** half: the widget needs a `bridgePort` setting and
a WebSocket client for this protocol, replacing the three existing connectors
(`handleTikTokEvent()`). Keep the existing data shapes so the alert logic does
not change. Not started.
