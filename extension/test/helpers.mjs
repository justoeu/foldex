import { readFileSync } from "node:fs";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";

const LOCALES_ROOT = join(dirname(fileURLToPath(import.meta.url)), "..", "_locales");

const MESSAGES = {};
for (const locale of ["en", "pt", "es"]) {
  MESSAGES[locale] = JSON.parse(
    readFileSync(join(LOCALES_ROOT, locale, "messages.json"), "utf8"),
  );
}

// Mirrors the callback style config.js expects: runtime.lastError is set
// synchronously during the call and cleared right after, like Chrome does.
// Tests default to pt because the SDD copy is written in pt-BR.
export function mockChrome({
  requestGranted = true,
  containsGranted = true,
  storageGetError = "",
  stored = {},
  tabs = [],
  locale = "pt",
} = {}) {
  const messages = MESSAGES[locale] ?? MESSAGES.en;
  const calls = { requests: [], contains: [], reads: [], writes: [], removals: [] };
  const chromeApi = {
    i18n: {
      getMessage(key, substitutions) {
        const entry = messages[key];
        if (!entry) return "";
        let text = entry.message;
        const list =
          substitutions == null ? [] : Array.isArray(substitutions) ? substitutions : [substitutions];
        list.forEach((value, i) => {
          text = text.replaceAll("$" + (i + 1), String(value));
        });
        return text;
      },
    },
    runtime: {
      openOptionsPage() {},
      getManifest: () => ({ version: "1.0.0" }),
    },
    tabs: {
      async query() {
        return tabs;
      },
    },
    permissions: {
      request(permission, callback) {
        calls.requests.push(permission);
        callback(requestGranted);
      },
      contains(permission, callback) {
        calls.contains.push(permission);
        callback(containsGranted);
      },
    },
    storage: {
      local: {
        get(defaults, callback) {
          calls.reads.push(defaults);
          if (storageGetError) chromeApi.runtime.lastError = { message: storageGetError };
          callback({ ...defaults, ...stored });
          delete chromeApi.runtime.lastError;
        },
        set(value, callback) {
          calls.writes.push(value);
          callback();
        },
        remove(keys, callback) {
          calls.removals.push(keys);
          callback?.();
        },
      },
    },
  };
  return { chromeApi, calls };
}

export function jsonResponse(body, init = {}) {
  return {
    ok: !(init.status >= 400),
    status: init.status ?? 200,
    json: async () => body,
    text: async () => (typeof body === "string" ? body : JSON.stringify(body)),
  };
}

export function mockFetch(handler) {
  const calls = [];
  const fetchImpl = async (url, options) => {
    const entry = { url, options };
    calls.push(entry);
    const resp = await handler(url, options, calls.length);
    entry.response = resp;
    return resp;
  };
  return { fetchImpl, calls };
}
