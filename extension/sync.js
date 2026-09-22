import { listTags } from "./api.js";
import { loadSettings } from "./storage.js";

export const SYNC_ALARM = "foldex:tag-sync";
export const SYNC_PERIOD_MINUTES = 60;

// The alarm exists whenever the service worker is awake; the gate is evaluated
// at fire time so flipping the pref needs no extra messaging.
export function shouldSyncTags(settings) {
  // A credential-less sync would 401 every hour for nothing.
  return Boolean(settings?.prefs?.sync && settings?.server && settings?.token);
}

function writeSyncedTags(payload, chromeApi) {
  return new Promise((resolve, reject) => {
    chromeApi.storage.local.set(payload, () => {
      const lastError = chromeApi.runtime && chromeApi.runtime.lastError;
      if (lastError) {
        reject(new Error(lastError.message));
        return;
      }
      resolve();
    });
  });
}

export async function syncTags({ chromeApi = chrome, fetchImpl = fetch, now = Date.now } = {}) {
  const settings = await loadSettings(chromeApi);
  if (!shouldSyncTags(settings)) return { synced: false, reason: "disabled" };

  try {
    const tags = await listTags(settings, { chromeApi, fetchImpl });
    const payload = { syncedTags: tags, syncedTagsAt: now() };
    await writeSyncedTags(payload, chromeApi);
    return { synced: true, tags: tags.length };
  } catch (error) {
    // The service worker has no UI to surface failures to — a failed hourly
    // sync just means the cache stays stale until the next tick.
    return { synced: false, reason: "error", error: error.message };
  }
}
