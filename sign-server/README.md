# Geseki Bridge, local TikTok sign server

A self-hosted sign server, so the plugin can connect to TikTok LIVE
**without a third-party service and without a shared rate limit**.

## Why this exists

`gotiktoklive` (the Go library the TikTok sidecar uses) cannot talk to TikTok
directly: every request must carry a valid `X-Bogus` / `X-Gnarly` / `msToken`
signature, which TikTok generates with obfuscated JavaScript inside a real
browser. The library can also delegate that to a remote service, but the one
it ships with is shared across all of its users and regularly runs out.

This server does what TikFinity does: it runs a headless Chrome, loads
tiktok.com, and borrows TikTok's own signing machinery from the page. No
third-party service, no quota.

## Proven working

The sidecar connects to a live room through this server, end to end:

```
[tiktok] using signer http://127.0.0.1:8090
{"ev":"state","state":"connecting"}
[tiktok] info: [Connected to websocket]
{"ev":"state","state":"connected"}
{"ev":"tiktok","event":"roomUser","data":{"viewerCount":278}}
{"ev":"tiktok","event":"chat","data":{"comment":"...","nickname":"..."}}
{"ev":"tiktok","event":"join","data":{"nickname":"...","userBadges":[...]}}
```

Chat, joins, viewer counts and badges all flow, signed entirely on this
machine.

## A 403 on /webcast/im/fetch is a rate limit, not a signature fault

The IM transport is rate-limited per address. Measured behaviour:

| Condition | `/webcast/im/fetch` | `room/info` |
|---|---|---|
| Fresh session, few requests | **200** + 30–45 KB protobuf | 200 |
| After dozens of requests in minutes | **403**, or 200 with a 0-byte body | still **200** |
| After ~3–5 min with no requests | **200** again | 200 |

`room/info` answering 200 while `im/fetch` is refused therefore means the
signature is fine and only the IM transport is limited.

Two mistakes keep the limit alive and are worth avoiding:

- **Probing `im/fetch` repeatedly creates the very 403 you are diagnosing.**
  Test with ONE request, then wait a few minutes.
- **Retrying at the ordinary reconnect cap does not let it lapse.** TikTok needs
  the address quiet for minutes; the sidecar used to retry every 30s forever,
  which re-armed the limit before it could clear. The sidecar now walks its own
  ladder for a refusal (2s → 10s → 1m → 5m → 10m) and then stops with a message
  instead of looping.

## What made it work

`gotiktoklive` builds its room-data URL as
`https://webcast.tiktok.com/webcast/` + `webcast/fetch/`, i.e.
`/webcast/webcast/fetch/`, a 404. The real IM transport endpoint is
`/webcast/im/fetch/`. Two rules had to be respected:

1. **The path must be `/webcast/im/fetch/`**, the doubled path is rewritten
   here before signing.
2. **The `User-Agent` header must match the `browser_version` query param.**
   The signer encodes its own UA into the signature; if the Go library sends a
   different `browser_version`, TikTok answers `403`. The server overwrites
   `browser_version` / `browser_name` / `browser_platform` in the incoming URL
   with values matching the signer's UA, then fetches with that same UA.

The request is fetched from **Node**, not from inside the page: a cross-origin
fetch in the page is blocked by CORS, while Node + the signed URL + the page's
cookies + the matching UA gets `HTTP 200` and a ~45 KB protobuf.

`gotiktoklive` also asks `GET /webcast/rate_limits` before connecting; a local
signer has no quota, so a generous ceiling is reported.

## Layout

| File | What |
|---|---|
| `server.mjs` | HTTP server: `/signature`, `/fetch`, `/webcast/fetch/`, `/webcast/rate_limits`, `/health`, `/restart` |
| `xgnarly.mjs` | `X-Gnarly` encoding helper |
| `javascript/` | TikTok's signing SDK, injected into the page locally |

## Running it

The bridge starts this server automatically when **Node.js is on `PATH`**; it
is not bundled. It is only a fallback: TikTok connects without it through a
signature-free endpoint, so a missing Node.js never blocks a connection.

To run it by hand:

```bash
npm install
# Point Puppeteer at an installed Chrome (it otherwise downloads its own):
#   PUPPETEER_EXECUTABLE_PATH=C:\Program Files\Google\Chrome\Application\chrome.exe
node --env-file-if-exists=.env server.mjs
```

Environment (`.env`):

| Key | Meaning |
|---|---|
| `PORT` | HTTP port, default `8090` |
| `PUPPETEER_EXECUTABLE_PATH` | Chrome to drive |
| `PROXY_ENABLED`, `PROXY_HOST`, `PROXY_USER`, `PROXY_PASS` | optional outbound proxy |

`node_modules/` and `.chrome-profile/` are not committed, run `npm install`
after cloning.

## Requirements

* **Node.js** on `PATH` (the bridge only starts the server when it finds it).
* **Google Chrome** installed, the signature is computed inside the browser
  by TikTok's own `frontierSign`. Set `PUPPETEER_EXECUTABLE_PATH` if Chrome is
  in a non-standard location.

## Upstream

Based on [carcabot/tiktok-signature](https://github.com/carcabot/tiktok-signature)
(MIT). The `/webcast/fetch/` and `/webcast/rate_limits` routes are our
additions, to match the contract `gotiktoklive` expects.
