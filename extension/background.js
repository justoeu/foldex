import { SYNC_ALARM, SYNC_PERIOD_MINUTES, syncTags } from "./sync.js";

// Non-persistent MV3: alarms wake this worker; the sync itself re-checks the
// prefs.sync gate at fire time (sync.js), so no listener state is kept here.
async function ensureAlarm() {
  const existing = await chrome.alarms.get(SYNC_ALARM);
  if (!existing) {
    chrome.alarms.create(SYNC_ALARM, { periodInMinutes: SYNC_PERIOD_MINUTES });
  }
}

chrome.runtime.onInstalled.addListener(ensureAlarm);
chrome.runtime.onStartup.addListener(ensureAlarm);

chrome.alarms.onAlarm.addListener((alarm) => {
  if (alarm.name === SYNC_ALARM) void syncTags();
});
