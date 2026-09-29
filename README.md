# Geseki Bridge

One OBS plugin that replaces **three** separate local services the
[Geseki / Sekisungkarak](https://github.com/sekisungkarak) stream widgets used to
need:

| Old service | Port | Now |
| --- | --- | --- |
| TikFinity WebSocket | `21213` | `tiktok` events from the bridge |
| IndoFinity WebSocket | `62024` | `tiktok` events from the bridge |
| SMTC Bridge (Python/Flask tray app) | `5000` | `nowplaying` events from the bridge |

Instead of three ports and three settings, widgets talk to **one** endpoint:

```
ws://127.0.0.1:47800/ws
```

The wire contract lives in [`docs/protocol.md`](docs/protocol.md) — read it
first.

## Why one plugin

* **One port.** A single loopback listener (WebSocket + a small HTTP surface).
* **No extra runtime.** SMTC (Windows media sessions) is read natively via
  WinRT inside the plugin; there is no tray app to install or keep running.
* **TikTok without a second port.** A small Go sidecar speaks
  newline-delimited JSON over **stdin/stdout**, so nothing extra is exposed.
* **Browser-based settings.** The Tools menu opens the plugin's own HTTP page,
  so the module carries no Qt dependency.

## HTTP surface

| Method | Path | Purpose |
| --- | --- | --- |
| `GET` | `/health` | liveness probe |
| `GET` | `/now-playing` | the current `nowplaying` payload (SMTC-Bridge compatible) |
| `GET` | `/artwork/<app_id>?v=<n>` | cached cover art |
| `GET` | `/` | settings page |
| `POST` | `/config` | save settings |

Because `GET /now-playing` matches the old SMTC Bridge schema, an existing
now-playing widget only needs its port changed to `47800` — no code change.

## Install

1. Grab the `geseki-bridge-<version>-windows-x64.zip` from
   [Releases](../../releases).
2. Extract `geseki-bridge` into your OBS plugins folder, i.e.
   `%ALLUSERSPROFILE%\obs-studio\plugins\`.
3. Restart OBS. A **Geseki Bridge Settings…** item appears under *Tools*.

The TikTok sidecar (`geseki-bridge-tiktok.exe`) must sit **next to**
`geseki-bridge.dll`; the plugin resolves it relative to its own module. The
release archive is already laid out this way.

## Build

Requires CMake 3.28+, Visual Studio 2022, the Windows 10/11 SDK, and Go 1.23 for
the sidecar. The CMake project downloads the OBS sources and prebuilt
dependencies declared in `buildspec.json` on first configure.

```powershell
# TikTok sidecar
cd sidecar/tiktok
go mod tidy
$env:GOOS="windows"; $env:GOARCH="amd64"
go build -trimpath -ldflags "-s -w" -o ../../bin/geseki-bridge-tiktok.exe .

# Plugin
cmake --preset windows-x64
cmake --build --preset windows-x64 --config RelWithDebInfo --parallel
cmake --install build_x64 --prefix release --config RelWithDebInfo
```

CI does all of the above on every tag (`v*`) — see
[`.github/workflows/release.yml`](.github/workflows/release.yml).

## Layout

```
docs/protocol.md            the wire contract (v1)
src/plugin-main.cpp         module entry + Tools menu item
src/bridge-server.cpp       WebSocket + HTTP + SMTC polling + TikTok wiring
src/json-util.*             tiny JSON emitters + parser
src/smtc-winrt.*            WinRT SMTC reader and transport control
src/tiktok-supervisor.*     sidecar process supervisor (pipes, no window)
sidecar/tiktok/             Go sidecar wrapping gotiktoklive
```

## Vendored dependency

`gotiktoklive` is **vendored** under `sidecar/tiktok/third_party/gotiktoklive`
(wired via a `replace` directive in `go.mod`) because upstream does not surface
data the widget needs. Our local patches, each marked `PATCH (upstream gap)` in
the source:

- **User avatars** — upstream checked `avatarLarge` but read `avatarJpg`; TikTok
  sends `avatarThumb`, so the guard failed and `ProfilePicture` was always
  empty. We now pick the largest image actually present.
- **User badges** — badges were exposed as a raw protobuf dump with no image
  URL. We expose `{image, name, color}` in the shape TikFinity emits.
- **Gift images** — `GiftEvent` had no image field, so `giftPictureUrl` was
  hardcoded empty. We surface `image > icon > giftLabelIcon`.

Covered by `sidecar/tiktok/third_party/gotiktoklive/giftpatch_test.go`.

## License

GPL-2.0-or-later (see [`LICENSE`](LICENSE)) — the same license OBS itself uses,
and required for the `obs-frontend-api` linkage.

The Go sidecar depends on
[`steampoweredtaco/gotiktoklive`](https://github.com/steampoweredtaco/gotiktoklive),
which is **MIT** licensed.
