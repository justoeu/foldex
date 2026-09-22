import { normalizeBaseUrl } from "./config.js";

export const DEFAULT_SETTINGS = Object.freeze({
  server: "http://localhost:9089",
  token: "",
  prefs: Object.freeze({ auto: true, close: true, sync: false }),
  defaultFolderId: null,
});

// Releases before the 2-panel rework stored {baseUrl, apiToken} (+ an even
// older sharedSecret); the new shape does not read them, so they are cleared
// to keep token material from lingering in storage.
const LEGACY_KEYS = ["baseUrl", "apiToken", "sharedSecret"];

function coercePrefs(rawPrefs) {
  const defaults = DEFAULT_SETTINGS.prefs;
  const prefs = {};
  for (const key of Object.keys(defaults)) {
    prefs[key] =
      typeof rawPrefs?.[key] === "boolean" ? rawPrefs[key] : defaults[key];
  }
  return prefs;
}

function coerceFolderId(rawFolderId) {
  return typeof rawFolderId === "number" && Number.isFinite(rawFolderId)
    ? rawFolderId
    : null;
}

export function normalizeSettings(rawSettings) {
  return {
    server: normalizeBaseUrl(rawSettings.server),
    token: String(rawSettings.token ?? "").trim(),
    prefs: coercePrefs(rawSettings.prefs),
    defaultFolderId: coerceFolderId(rawSettings.defaultFolderId),
  };
}

function callStorage(chromeApi, method, value) {
  return new Promise((resolve, reject) => {
    chromeApi.storage.local[method](value, (result) => {
      const lastError = chromeApi.runtime && chromeApi.runtime.lastError;
      if (lastError) {
        reject(new Error(lastError.message));
        return;
      }
      resolve(result);
    });
  });
}

export async function loadSettings(chromeApi) {
  const stored = await callStorage(chromeApi, "get", {
    server: DEFAULT_SETTINGS.server,
    token: DEFAULT_SETTINGS.token,
    prefs: { ...DEFAULT_SETTINGS.prefs },
    defaultFolderId: DEFAULT_SETTINGS.defaultFolderId,
  });

  try {
    if (typeof chromeApi.storage.local.remove === "function") {
      chromeApi.storage.local.remove(LEGACY_KEYS);
    }
  } catch {
    // Cleanup is best-effort; never block startup on it.
  }

  // Stored garbage must not brick the popup on open — invalid servers fall
  // back to the safe default; saveSettings stays strict instead.
  let server;
  try {
    server = normalizeBaseUrl(stored.server);
  } catch {
    server = DEFAULT_SETTINGS.server;
  }

  return {
    server,
    token: String(stored.token ?? "").trim(),
    prefs: coercePrefs(stored.prefs),
    defaultFolderId: coerceFolderId(stored.defaultFolderId),
  };
}

export function saveSettings(settings, chromeApi) {
  const normalized = normalizeSettings(settings);
  return callStorage(chromeApi, "set", normalized).then(() => normalized);
}
