import { describe, test } from "node:test";
import assert from "node:assert/strict";

import {
  DEFAULT_SETTINGS,
  loadSettings,
  normalizeSettings,
  saveSettings,
} from "../storage.js";

function storageChrome({ stored = {}, getError = "", setError = "" } = {}) {
  const calls = { reads: [], writes: [], removals: [] };
  const chromeApi = {
    runtime: {},
    storage: {
      local: {
        get(defaults, callback) {
          calls.reads.push(defaults);
          if (getError) chromeApi.runtime.lastError = { message: getError };
          callback({ ...defaults, ...stored });
          delete chromeApi.runtime.lastError;
        },
        set(value, callback) {
          calls.writes.push(value);
          if (setError) chromeApi.runtime.lastError = { message: setError };
          callback();
          delete chromeApi.runtime.lastError;
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

describe("settings defaults", () => {
  test("empty storage loads the shipped defaults", async () => {
    const { chromeApi, calls } = storageChrome();
    const settings = await loadSettings(chromeApi);

    assert.deepEqual(settings, {
      server: "http://localhost:9089",
      token: "",
      prefs: { auto: true, close: true, sync: false },
      defaultFolderId: null,
    });
    assert.equal(calls.reads.length, 1);
  });

  test("stored values survive a load round trip", async () => {
    const { chromeApi } = storageChrome({
      stored: {
        server: "https://foldex.example/app",
        token: "fx_tok",
        prefs: { auto: false, close: true, sync: true },
        defaultFolderId: 3,
      },
    });
    const settings = await loadSettings(chromeApi);

    assert.deepEqual(settings, {
      server: "https://foldex.example/app",
      token: "fx_tok",
      prefs: { auto: false, close: true, sync: true },
      defaultFolderId: 3,
    });
  });
});

describe("settings normalization", () => {
  test("save normalizes server and token and persists exactly the four keys", async () => {
    const { chromeApi, calls } = storageChrome();
    const saved = await saveSettings(
      {
        server: " HTTPS://Foldex.Example:443/app/// ",
        token: " fx_token ",
        prefs: { auto: false, close: true, sync: false },
        defaultFolderId: null,
      },
      chromeApi,
    );

    assert.equal(saved.server, "https://foldex.example/app");
    assert.equal(saved.token, "fx_token");
    assert.deepEqual(calls.writes, [
      {
        server: "https://foldex.example/app",
        token: "fx_token",
        prefs: { auto: false, close: true, sync: false },
        defaultFolderId: null,
      },
    ]);
  });

  test("partial or corrupt prefs fall back per-key; non-numeric folders become null", () => {
    const settings = normalizeSettings({
      server: "http://localhost:9089",
      token: "",
      prefs: { auto: false, close: "yes", sync: undefined },
      defaultFolderId: "não numérico",
    });

    assert.deepEqual(settings.prefs, { auto: false, close: true, sync: false });
    assert.equal(settings.defaultFolderId, null);

    const numeric = normalizeSettings({
      server: "http://localhost:9089",
      token: "",
      prefs: {},
      defaultFolderId: 7,
    });
    assert.equal(numeric.defaultFolderId, 7);
    assert.deepEqual(numeric.prefs, DEFAULT_SETTINGS.prefs);
  });

  test("an invalid stored server falls back to the default instead of breaking startup", async () => {
    const { chromeApi } = storageChrome({
      stored: { server: "http://nas.lan:9089", token: "fx_tok" },
    });
    const settings = await loadSettings(chromeApi);

    assert.equal(settings.server, DEFAULT_SETTINGS.server);
    assert.equal(settings.token, "fx_tok");
  });
});

describe("storage failure semantics", () => {
  test("read failures reject so callers can render them", async () => {
    const { chromeApi } = storageChrome({ getError: "storage read failed" });

    await assert.rejects(loadSettings(chromeApi), /storage read failed/);
  });

  test("write failures reject instead of reporting settings as saved", async () => {
    const { chromeApi, calls } = storageChrome({ setError: "quota exceeded" });

    await assert.rejects(
      saveSettings(
        { server: "https://foldex.example", token: "", prefs: {}, defaultFolderId: null },
        chromeApi,
      ),
      /quota exceeded/,
    );
    assert.equal(calls.writes.length, 1);
  });

  test("legacy keys from earlier versions are cleared best-effort on load", async () => {
    const { chromeApi, calls } = storageChrome({
      stored: { baseUrl: "http://localhost:9089", apiToken: "fx_old" },
    });
    await loadSettings(chromeApi);

    assert.deepEqual(calls.removals, [["baseUrl", "apiToken", "sharedSecret"]]);
  });
});
