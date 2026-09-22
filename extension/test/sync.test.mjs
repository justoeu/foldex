import { describe, test } from "node:test";
import assert from "node:assert/strict";

import {
  SYNC_ALARM,
  SYNC_PERIOD_MINUTES,
  shouldSyncTags,
  syncTags,
} from "../sync.js";
import { jsonResponse, mockChrome, mockFetch } from "./helpers.mjs";

const ON = {
  server: "https://foldex.example",
  token: "fx_token",
  prefs: { auto: true, close: false, sync: true },
  defaultFolderId: null,
};

describe("tag sync gating", () => {
  test("shouldSyncTags requires the pref AND usable credentials", () => {
    assert.equal(shouldSyncTags(ON), true);
    assert.equal(shouldSyncTags({ ...ON, prefs: { ...ON.prefs, sync: false } }), false);
    assert.equal(shouldSyncTags({ ...ON, token: "" }), false);
    assert.equal(shouldSyncTags(null), false);
  });

  test("a disabled pref makes the alarm handler a no-op: zero fetches, zero writes", async () => {
    const { chromeApi, calls } = mockChrome({ stored: { ...ON, prefs: { ...ON.prefs, sync: false } } });
    const { fetchImpl, calls: fetchCalls } = mockFetch(() => jsonResponse([]));

    const result = await syncTags({ chromeApi, fetchImpl });

    assert.deepEqual(result, { synced: false, reason: "disabled" });
    assert.equal(fetchCalls.length, 0);
    assert.equal(calls.writes.length, 0);
  });

  test("an enabled sync fetches tags and persists them with the sync timestamp", async () => {
    const { chromeApi, calls } = mockChrome({ stored: ON });
    const { fetchImpl } = mockFetch(() => jsonResponse([{ id: 1, name: "IA" }]));
    let clock = 1_000;

    const result = await syncTags({ chromeApi, fetchImpl, now: () => (clock += 500) });

    assert.deepEqual(result, { synced: true, tags: 1 });
    assert.deepEqual(calls.writes, [
      { syncedTags: [{ id: 1, name: "IA" }], syncedTagsAt: 1500 },
    ]);
  });

  test("a failed fetch reports the error and writes nothing", async () => {
    const { chromeApi, calls } = mockChrome({ stored: ON });
    const dead = async () => {
      throw new TypeError("net::ERR_CONNECTION_REFUSED");
    };

    const result = await syncTags({ chromeApi, fetchImpl: dead });

    assert.equal(result.synced, false);
    assert.equal(result.reason, "error");
    assert.match(result.error, /CONNECTION_REFUSED/);
    assert.equal(calls.writes.length, 0);
  });

  test("alarm contract is hourly under the foldex namespace", () => {
    assert.equal(SYNC_ALARM, "foldex:tag-sync");
    assert.equal(SYNC_PERIOD_MINUTES, 60);
  });
});
