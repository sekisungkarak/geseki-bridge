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
* **Native settings dialog.** The Tools menu opens a Qt dialog inside OBS to
  edit the TikTok username, API key, auto-connect and the port. Qt is linked
  from OBS — nothing Qt is bundled.
* **Active Audio Sources page.** `http://127.0.0.1:47800/sessions` lists the
  app ids of every active Windows media session — the same content the old SMTC
  Bridge `/sessions` page showed.

## HTTP surface

| Method | Path | Purpose |
| --- | --- | --- |
| `GET` | `/health` | liveness probe |
| `GET` | `/now-playing` | the current `nowplaying` payload (SMTC-Bridge compatible) |
| `GET` | `/artwork/<app_id>?v=<n>` | cached cover art |
| `GET` | `/sessions` | **Active Audio Sources** page (live list of media sessions) |

Because `GET /now-playing` matches the old SMTC Bridge schema, an existing
now-playing widget only needs its port changed to `47800` — no code change.

## Install

Two downloads are published for Windows:

| Asset | Use it when |
| --- | --- |
| `geseki-bridge-<version>-windows-installer.zip` | **Easiest.** Unzip, run the `.exe`, done — it finds your OBS and copies the files in. |
| `geseki-bridge-<version>-windows-x64.zip` | Manual install (e.g. a portable OBS you want to keep self-contained). |

### Option A — installer (recommended)

1. Download `geseki-bridge-<version>-windows-installer.zip` and unzip it.
2. Run `geseki-bridge-<version>-windows-installer.exe`. It auto-detects your
   OBS folder (the `HKLM\SOFTWARE\OBS Studio` registry value written by the
   official installer, falling back to `C:\Program Files\obs-studio`) — or
   browse to a **portable** OBS folder when prompted.
3. Restart OBS.

### Option B — manual zip

The zip mirrors the OBS folder tree, so there is **no folder to guess** — you
extract it straight into OBS.

1. Grab `geseki-bridge-<version>-windows-x64.zip` from
   [Releases](../../releases).
2. Extract it into your **OBS install folder** (the one containing
   `obs64.exe`). The zip contains `obs-plugins/` and `data/`; merging them
   gives:
   ```
   <OBS>\obs-plugins\64bit\geseki-bridge.dll
   <OBS>\obs-plugins\64bit\geseki-bridge-tiktok.exe
   <OBS>\data\obs-plugins\geseki-bridge\locale\en-US.ini
   ```
   * **Portable OBS:** this is the folder you unzipped OBS into (e.g.
     `E:\OBS-VERT`).
   * **Installer OBS:** `C:\Program Files\obs-studio` — copy `obs-plugins\`
     and `data\` into it and approve the admin prompt.
3. Restart OBS. A **Geseki Bridge** item appears under *Tools*; it opens a
   dialog to set the TikTok username, API key, auto-connect and the port.

> **Why not `%ProgramData%\obs-studio\plugins`?** OBS *portable* mode ignores
> that folder entirely (`AddExtraModulePaths()` returns early when
> `portable_mode` is set), so a plugin dropped there only works for installer
> OBS. Both options above write into the OBS folder itself, which works for
> both.

The TikTok sidecar (`geseki-bridge-tiktok.exe`) must sit **next to**
`geseki-bridge.dll`; the plugin resolves it relative to its own module. Both
the installer and the zip already lay it out this way.

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

Or just run the helper, which also installs into a portable OBS and (with
`-Installer`) builds the Inno Setup `.exe`:

```powershell
powershell -ExecutionPolicy Bypass -File tools\build-local.ps1 -Install E:\OBS-VERT -Installer
```

CI does all of the above on every tag (`v*`) — see
[`.github/workflows/release.yml`](.github/workflows/release.yml). It publishes
both the manual zip and the installer.

## Layout

```
docs/protocol.md            the wire contract (v1)
src/plugin-main.cpp         module entry + Tools menu item
src/settings-dialog.*       native Qt6 settings dialog
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
  URL. We expose `{image, name, color}` in the shape TikFinity emits. Two extra
  fixes: the label lives in a `TextBadge` field the vendored `.proto` does not
  declare (field 2, e.g. "No. 3"), so we read it from the unknown bytes; and
  TikTok sends some artwork twice (an IMAGE entry plus a labelled COMBINE
  entry), which drew a doubled badge, so identical images are collapsed.
- **Gift images** — `GiftEvent` had no image field, so `giftPictureUrl` was
  hardcoded empty. We surface `image > icon > giftLabelIcon`.
- **Emotes** — the comment's `emotesList` (inline emotes) and the standalone
  `WebcastEmoteChatMessage` (subscriber stickers) were never read, so an emote
  reached the widget as a bare placeholder character. We now emit
  `{emoteId, emoteImageUrl, placeInComment, emoteType, emotePrivateType}` on
  `chat`. `placeInComment` is the 0-based index of the placeholder char,
  matching TikTok Live Connector's contract. A standalone subscriber emote has
  no comment text, so the bridge re-shapes it into a synthetic `chat` frame
  (one zero-width placeholder per emote) — the widget's existing chat renderer
  draws it with no widget-side change. A subscriber emote that arrives through
  the chat path with an empty `comment` is padded with placeholders up to its
  last emote index, otherwise the renderer drops every emote as out-of-range.

Covered by `sidecar/tiktok/third_party/gotiktoklive/giftpatch_test.go`,
`badgepatch_test.go` and `emotepatch_test.go`, plus `sidecar/tiktok/emoteaschat_test.go`.

## License

GPL-2.0-or-later (see [`LICENSE`](LICENSE)) — the same license OBS itself uses,
and required for the `obs-frontend-api` linkage.

The Go sidecar depends on
[`steampoweredtaco/gotiktoklive`](https://github.com/steampoweredtaco/gotiktoklive),
which is **MIT** licensed.
