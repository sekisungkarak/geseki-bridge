# Geseki Bridge — Wire Protocol (v1)

Geseki Bridge is an OBS plugin that exposes **one local endpoint** for every
widget in the Geseki / Sekisungkarak suite. It replaces the three separate
local services the widgets used to talk to:

| Old | Port | Replaced by |
| --- | --- | --- |
| TikFinity WebSocket | `21213` | `tiktok` events over the bridge |
| IndoFinity WebSocket | `62024` | `tiktok` events over the bridge |
| SMTC Bridge (Python/Flask) | `5000` | `nowplaying` events over the bridge |

Widgets no longer need three settings; they need one: the bridge port.

---

## 1. Transport

* **WebSocket** server hosted by the plugin.
* Default address: `ws://127.0.0.1:47800/ws`
* The port is configurable (plugin settings). Bind is loopback only.
* One text frame = one JSON message. No batching, no binary frames.

The plugin also answers a small HTTP surface (outside the upgrade):

| Method | Path | Purpose |
| --- | --- | --- |
| `GET` | `/health` | liveness probe |
| `GET` | `/now-playing` | the current `nowplaying` **data** object, for the legacy SMTC-Bridge widget that polls this endpoint |
| `GET` | `/artwork/<app_id>?v=<n>` | cached cover art bytes |
| `GET` | `/` | the browser-based settings page |

`GET /health` returns:

```json
{ "ok": true, "bridge": "geseki-bridge/0.1.0", "protocol": 1 }
```

`GET /now-playing` returns the same object as the `nowplaying` message's
`data` field (below), so an existing SMTC-Bridge widget keeps working once it
is pointed at this port — no WebSocket needed.

---

## 2. Server → client

Every message has a `type` field. Unknown types MUST be ignored by clients so
the bridge can add features without breaking older widgets.

### 2.1 `hello`

Sent once, immediately after the socket opens.

```json
{
  "type": "hello",
  "protocol": 1,
  "bridge": "geseki-bridge/0.1.0",
  "capabilities": ["tiktok", "nowplaying"]
}
```

### 2.2 `status`

Sent on connect and whenever a subsystem changes state. Lets a widget show
"TikTok: offline" instead of silently showing nothing.

```json
{
  "type": "status",
  "tiktok": { "state": "connected", "username": "sekisungkarak", "message": "" },
  "nowplaying": { "state": "idle", "message": "" }
}
```

`state` is one of `off`, `connecting`, `connected`, `error`.

### 2.3 `tiktok`

A TikTok LIVE event, normalised to the shape the widgets already understand.

```json
{ "type": "tiktok", "event": "chat", "data": { ... } }
```

`event` is one of:

| `event`   | Meaning                        | Extra keys on `data` |
| --------- | ------------------------------ | -------------------- |
| `chat`    | a viewer posted a comment      | `comment`, `emotes` |
| `emote`   | a subscriber sent an emote (sticker) | `emotes` |
| `gift`    | a gift was sent                | `giftName`, `giftPictureUrl`, `repeatCount`, `repeatEnd`, `giftType` |
| `follow`  | a viewer followed              | — |
| `share`   | a viewer shared the stream     | — |
| `subscribe` | a viewer subscribed          | — |
| `like`    | likes were sent                | `likeCount`, `totalLikes` |
| `roomUser`| viewer count changed           | `viewerCount` |
| `join`    | a viewer entered the room      | — |

**Common `data` fields** (present when the source event carries a user):

| Key | Type | Notes |
| --- | --- | --- |
| `userId` | string | stable id, used for de-duplication |
| `uniqueId` | string | `@handle` without the `@` |
| `nickname` | string | display name |
| `profilePictureUrl` | string | avatar URL |
| `userBadges` | array | see below |

`userBadges` entries are already flattened for the widget, which reads
`image` / `name` / `color`:

```json
{ "image": "https://…png", "name": "Top Gifter", "color": "#ffcc00" }
```

### 2.3.1 `emotes`

Both `chat` (a comment that contains emotes) and `emote` (a subscriber
sticker, sent as its own event with no comment text) carry an `emotes` array,
flattened to the shape the widget renders:

```json
{
  "emoteId": "7089",
  "emoteImageUrl": "https://…png",
  "placeInComment": 4,
  "emoteType": 0,
  "emotePrivateType": 1
}
```

* `placeInComment` is the **0-based** index of the placeholder character the
  emote replaces inside `comment` (same contract as TikTok Live Connector).
  TikTok sends one placeholder char per emote; the widget swaps it for the
  image at that index. Absent for the standalone `emote` event.
* `emotePrivateType`: `0` normal, `1` subscriber wave (`SUB_WAVE`) — how a
  subscriber emote is flagged.
* The array is always present (possibly empty) on `chat`; widgets that ignore
  it render the placeholder character as before.

TikFinity never surfaced this data (its `emotes` array is always empty), so a
subscriber emote used to reach the widgets as a bare placeholder glyph.

### 2.4 `nowplaying`

The current media session (Windows SMTC), in the same shape the SMTC Bridge
REST payload used, so existing widget code keeps working.

```json
{
  "type": "nowplaying",
  "data": {
    "app_version": "0.1.0",
    "current_session_id": "Spotify.exe",
    "sessions": [
      {
        "source_app_id": "Spotify.exe",
        "media_properties": {
          "Title": "…", "Artist": "…", "AlbumTitle": "…", "AlbumArtist": "…",
          "Thumbnail": "http://127.0.0.1:47800/artwork/Spotify.exe?v=1699999999999",
          "AlbumTrackCount": 0, "TrackNumber": 0, "Genres": [], "Subtitle": ""
        },
        "playback_info": {
          "PlaybackStatus": 4, "PlaybackType": 1, "PlaybackRate": 1.0,
          "IsShuffleActive": false, "AutoRepeatMode": 0
        },
        "timeline_properties": {
          "Position": 41000, "StartTime": 0, "EndTime": 200000,
          "MinSeekTime": 0, "MaxSeekTime": 200000,
          "LastUpdatedTime": "2026-09-29T12:00:00+00:00"
        }
      }
    ]
  }
}
```

Notes:

* All timeline values are **milliseconds**.
* `PlaybackStatus`: `0 CLOSED`, `1 OPENED`, `2 CHANGING`, `3 STOPPED`,
  `4 PLAYING`, `5 PAUSED`.
* `PlaybackType`: `0 UNKNOWN`, `1 MUSIC`, `2 VIDEO`, `3 IMAGE`.
* Artwork is served by the bridge itself at `GET /artwork/<safe_app_id>` with a
  cache-busting `?v=<n>` — **not** embedded as base64, so a big cover does not
  inflate every poll. `<n>` is a monotonic counter that only changes when the
  bytes change, so the URL is safe to cache.
* The payload is pushed when it changes (and at least every 1 s while a session
  is playing), not on a request/response basis.
* The accent colour is **not** computed by the bridge: the widget derives its
  own palette from the artwork URL, exactly as it does today.

### 2.5 `pong`

Reply to a client `ping`. Used by widgets to detect a dead bridge.

```json
{ "type": "pong", "t": 1699999999999 }
```

---

## 3. Client → server

### 3.1 `ping`

```json
{ "type": "ping", "t": 1699999999999 }
```

### 3.2 `subscribe`

Widgets that only care about one subsystem can say so. Omitting it means
"everything".

```json
{ "type": "subscribe", "events": ["tiktok", "nowplaying"] }
```

### 3.3 `tiktok.connect` / `tiktok.disconnect`

Drives the TikTok sidecar. `apiKey` is optional (see §4).

```json
{ "type": "tiktok.connect", "username": "sekisungkarak", "apiKey": "" }
{ "type": "tiktok.disconnect" }
```

### 3.4 `smtc.control`

Remote-control the focused media session.

```json
{ "type": "smtc.control", "action": "play" }
```

`action` is one of `play`, `pause`, `toggle`, `next`, `previous`, `seek`.
`seek` takes `"position": <ms>`.

---

## 4. TikTok engine

The TikTok sidecar is a small Go binary wrapping
[`gotiktoklive`](https://github.com/steampoweredtaco/gotiktoklive) (MIT).

* The plugin starts it, keeps it alive, and restarts it if it exits.
* It talks to the plugin over **stdin/stdout** (newline-delimited JSON), not a
  second port, so there is nothing extra to firewall.
* `gotiktoklive` signs its requests through the Euler signer
  (`https://tiktok.eulerstream.com`). An API key raises the rate limit; the
  sidecar runs without one, at the lower default limit. The key is stored in
  the plugin's config, never in a widget URL.

Sidecar protocol (internal, not public API):

| Direction | Message |
| --- | --- |
| plugin → sidecar | `{"cmd":"connect","username":"…","apiKey":"…"}` |
| plugin → sidecar | `{"cmd":"disconnect"}` |
| plugin → sidecar | `{"cmd":"quit"}` |
| sidecar → plugin | `{"ev":"ready"}` |
| sidecar → plugin | `{"ev":"state","state":"connected"\|"connecting"\|"off"\|"error","message":"…"}` |
| sidecar → plugin | `{"ev":"tiktok","event":"chat","data":{…}}` |

The plugin forwards `tiktok` payloads to WebSocket clients verbatim, so the
normalisation lives in exactly one place: the sidecar.

---

## 5. Versioning

`protocol` is an integer, bumped only on a breaking change. Additive fields and
new `type` values are NOT breaking. Widgets must ignore unknown `type` values
and unknown keys.
