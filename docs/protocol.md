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
| `GET` | `/bridge-port` | port discovery: which port the WebSocket is on |
| `GET` | `/now-playing` | the current `nowplaying` **data** object, for the legacy SMTC-Bridge widget that polls this endpoint |
| `GET` | `/artwork/<app_id>?v=<n>` | cached cover art bytes |
| `GET` | `/` | the browser-based settings page |

`GET /health` returns:

```json
{ "ok": true, "bridge": "geseki-bridge/0.2.0", "protocol": 1 }
```

`GET /bridge-port` returns:

```json
{ "ok": true, "bridge": "geseki-bridge/0.2.0", "protocol": 1, "wsPort": 47800, "discoveryPort": 47800 }
```

### Port discovery

The WebSocket port is configurable, and a widget cannot read the plugin's
config. So the bridge also listens on a **fixed** discovery port `47800` and
answers `/bridge-port` there with the real `wsPort`. A widget asks that fixed
port first and uses the answer, so changing the port in the plugin needs no
edit in any widget. When the WebSocket itself already uses `47800`, the main
listener serves `/bridge-port` too (one port, both roles). If `47800` is taken
by another program, discovery is unavailable and a widget falls back to the
port in its own URL.

Both responses are `Access-Control-Allow-Origin: *` and carry
`Access-Control-Allow-Private-Network: true`, because a widget is served over
`https` and fetches `http://127.0.0.1` (a public→private request Chromium
otherwise blocks).

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
  "bridge": "geseki-bridge/0.2.0",
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

`tiktok.message` carries a human-readable reason when there is one (for example
`reconnecting` while the sidecar retries a dropped socket, or `stream ended`).

**Reconnect.** The TikTok sidecar reconnects on its own when the live socket
drops (network blip, TikTok closing the socket, an expired cursor). It retries
with exponential backoff — 2 s, 4 s, 8 s, 16 s, then 30 s — and reports
`connecting` / `reconnecting` while it does. A session that stayed up for at
least 30 s resets the backoff, so a long, healthy session reconnects quickly
after a single blip. It stops only when TikTok reports the room is gone
(stream ended) or the handle does not resolve, and then reports `off`. A new
`tiktok.connect`, a `tiktok.disconnect`, or `quit` cancels a pending retry.

### 2.3 `tiktok`

A TikTok LIVE event, normalised to the shape the widgets already understand.

```json
{ "type": "tiktok", "event": "chat", "data": { ... } }
```

`event` is one of:

| `event`   | Meaning                        | Extra keys on `data` |
| --------- | ------------------------------ | -------------------- |
| `chat`    | a viewer posted a comment, **or a subscriber sent an emote (sticker)** | `comment`, `emotes` |
| `gift`    | a gift was sent                | `giftName`, `giftPictureUrl`, `repeatCount`, `repeatEnd`, `giftType` |
| `follow`  | a viewer followed              | — |
| `share`   | a viewer shared the stream     | — |
| `subscribe` | a viewer subscribed (new sub or renewal) | — |
| `superFan` | a viewer became a Super Fan | — |
| `superFanJoin` | an existing Super Fan entered the room | — |
| `superFanBox` | a viewer sent a Super Fan Box | `diamondCount` |
| `like`    | likes were sent                | `likeCount`, `totalLikes` |
| `roomUser`| viewer count changed           | `viewerCount` |
| `join`    | a viewer entered the room      | — |

`subscribe` is produced from two TikTok messages: `WebcastSubNotifyMessage` (a
subscription notice) and `WebcastMemberMessage` with action `SUBSCRIBED`. The
event is emitted with the common user fields and no extra keys, matching what
the widgets already render for their subscribe alert.

**Super Fan** is a separate event family, named after TikTok Live Connector's
`superFan` / `superFanJoin` / `superFanBox`. It is **not** a subscribe: a Super
Fan is a paid tier, and TikTok signals it on its own messages.

* `superFan` and `superFanJoin` come from `WebcastBarrageMessage`, classified by
  its display-text key (`content.key`, falling back to `commonBarrageContent.key`,
  field 24): a key containing `ttlive_superfan_commentnotif_superfanjoined` is a
  join, any other key containing `ttlive_superfan` is a new Super Fan. The sender
  rides in field 50 (`user`), which the vendored proto does not declare, so the
  bridge reads it straight off the wire.
* `superFanBox` comes from `WebcastEnvelopeMessage` when its display-text key
  contains `ttlive_superfanbox` or `envelopeInfo.businessType` is
  `SUPER_FAN_BOX` (19). It carries `diamondCount`; the sender is in the envelope's
  `sendUser*` fields.

The old `subscribe` event is unchanged and still emitted — Super Fan does not
replace it. A barrage that carries no Super Fan marker is dropped, not forwarded.

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

`chat` carries an `emotes` array, flattened to the shape the widget renders:

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
  image at that index.
* `emotePrivateType`: `0` normal, `1` subscriber wave (`SUB_WAVE`) — how a
  subscriber emote is flagged.
* The array is always present (possibly empty) on `chat`; widgets that ignore
  it render the placeholder character as before.

TikFinity never surfaced this data (its `emotes` array is always empty), so a
subscriber emote used to reach the widgets as a bare placeholder glyph.

**Subscriber emotes are delivered as `chat`.** TikTok sends a subscriber emote
(sticker) as its own message with no comment text, but the widgets only render
emotes that sit inside a comment. The bridge therefore re-shapes it into a
synthetic `chat` frame: `comment` is one placeholder character per emote
(U+200B, zero-width) and each emote's `placeInComment` is its 0-based position.
That way an existing `chat` renderer draws the artwork with no widget-side
change. There is no separate `emote` event.

TikTok sometimes delivers a subscriber emote through the **chat** path itself
with an **empty `comment`** while its emotes still carry indexes 0,1,2,… A
renderer that drops emotes whose index falls outside the text would discard all
of them, so the bridge pads `comment` with zero-width spaces up to the last
index whenever the comment is shorter than the emote positions require. A
comment that already carries its own placeholders is left untouched.

### 2.4 `nowplaying`

The current media session (Windows SMTC), in the same shape the SMTC Bridge
REST payload used, so existing widget code keeps working.

```json
{
  "type": "nowplaying",
  "data": {
    "app_version": "0.2.0",
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
