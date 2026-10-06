// ============================================================================
// Sign in to TikTok for the local signer.
//
// TikTok requires a logged-in session for the live chat endpoint
// (/webcast/im/fetch/). Without one the signer gets HTTP 403 on every chat
// request and the plugin cannot connect. This script opens Chrome with the
// SAME profile the signer uses, so the cookies you create here are picked up
// by the signer automatically.
//
//   node sign-in.mjs
//
// Log in in the window that opens (QR code, phone or email). The window
// closes by itself once the session cookie appears. Nothing is written to
// disk except Chrome's own profile, and no credential passes through this
// script.
// ============================================================================
import fs from "node:fs";
import path from "node:path";
import { fileURLToPath } from "node:url";
import { spawn } from "node:child_process";

const __dirname = path.dirname(fileURLToPath(import.meta.url));

// Must match server.mjs: SIGNER_PROFILE_DIR wins, else ./.chrome-profile.
const PROFILE_DIR = process.env.SIGNER_PROFILE_DIR
  ? path.resolve(process.env.SIGNER_PROFILE_DIR)
  : path.join(__dirname, ".chrome-profile");

// Must match DEFAULT_UA in server.mjs — the signature is tied to the UA.
const UA =
  "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/18.6 Safari/605.1.15";

function chromePath() {
  const candidates = [
    process.env.PUPPETEER_EXECUTABLE_PATH,
    "C:\\Program Files\\Google\\Chrome\\Application\\chrome.exe",
    "C:\\Program Files (x86)\\Google\\Chrome\\Application\\chrome.exe",
    path.join(
      process.env.LOCALAPPDATA || "",
      "Google\\Chrome\\Application\\chrome.exe",
    ),
  ].filter(Boolean);
  for (const c of candidates) if (fs.existsSync(c)) return c;
  return null;
}

const chrome = chromePath();
if (!chrome) {
  console.error(
    "Google Chrome was not found. Set PUPPETEER_EXECUTABLE_PATH to chrome.exe.",
  );
  process.exit(1);
}

fs.mkdirSync(PROFILE_DIR, { recursive: true });

// The signer drives Chrome with this same profile. Chrome allows only one
// instance per profile, so the signer must be stopped first — otherwise the
// login window opens against a locked profile and the cookies land in a
// throwaway context. OBS owns the signer, so OBS has to be closed.
const lock = path.join(PROFILE_DIR, "SingletonLock");
if (fs.existsSync(lock)) {
  console.log("NOTE: a Chrome instance may still be using this profile.");
  console.log("      Close OBS (and any Chrome using the signer profile) first.");
  console.log();
}

console.log("Opening Chrome to sign in to TikTok.");
console.log("  profile :", PROFILE_DIR);
console.log("  chrome  :", chrome);
console.log();
console.log("Sign in in the window that opens. It closes by itself when done.");
console.log();

// Launch detached: the signer keeps this profile, and Chrome must not die with
// this process. The signer is stopped first by the caller (or not running).
const child = spawn(
  chrome,
  [
    `--user-data-dir=${PROFILE_DIR}`,
    "--no-first-run",
    "--no-default-browser-check",
    "--disable-blink-features=AutomationControlled",
    `--user-agent=${UA}`,
    "https://www.tiktok.com/login",
  ],
  { detached: true, stdio: "ignore" },
);
child.unref();

console.log("Chrome started (pid %d).", child.pid);
console.log();
console.log("After you are logged in, close Chrome completely, then restart OBS");
console.log("so the signer picks up the session.");
