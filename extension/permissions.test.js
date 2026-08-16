import { describe, expect, test } from "bun:test";

import {
  getStoredConfig,
  normalizeBaseUrl,
  permissionForBaseUrl,
} from "./config.js";
import { initOptionsPage, saveOptions, testConnection } from "./options.js";
import { initPopup, loadTags, saveLink } from "./popup.js";

function fakeElement() {
  const listeners = new Map();
  return {
    value: "",
    textContent: "",
    className: "",
    disabled: false,
    innerHTML: "",
    style: {},
    dataset: {},
    classList: { add() {}, remove() {} },
    addEventListener(type, listener) {
      listeners.set(type, listener);
    },
    appendChild() {},
    dispatch(type, event = { preventDefault() {} }) {
      return listeners.get(type)?.(event);
    },
  };
}

function fakeDocument(ids) {
  const elements = Object.fromEntries(ids.map((id) => [id, fakeElement()]));
  const listeners = new Map();
  return {
    elements,
    documentApi: {
      getElementById(id) {
        return elements[id];
      },
      createElement() {
        return fakeElement();
      },
      addEventListener(type, listener) {
        listeners.set(type, listener);
      },
      dispatch(type, event) {
        return listeners.get(type)?.(event);
      },
    },
  };
}

function mockChrome({
  requestGranted = true,
  containsGranted = true,
  storageGetError = "",
  storageSetError = "",
} = {}) {
  const calls = {
    requests: [],
    contains: [],
    reads: [],
    writes: [],
    events: [],
  };
  const chromeApi = {
    runtime: { openOptionsPage() {} },
    tabs: {
      async query() {
        return [];
      },
    },
    permissions: {
      request(permission, callback) {
        calls.requests.push(permission);
        calls.events.push("request");
        callback(requestGranted);
      },
      contains(permission, callback) {
        calls.contains.push(permission);
        calls.events.push("contains");
        callback(containsGranted);
      },
    },
    storage: {
      local: {
        get(value, callback) {
          calls.reads.push(value);
          calls.events.push("get");
          if (storageGetError)
            chromeApi.runtime.lastError = { message: storageGetError };
          callback(value);
          delete chromeApi.runtime.lastError;
        },
        set(value, callback) {
          calls.writes.push(value);
          calls.events.push("set");
          if (storageSetError)
            chromeApi.runtime.lastError = { message: storageSetError };
          callback();
          delete chromeApi.runtime.lastError;
        },
      },
    },
  };
  return { chromeApi, calls };
}

function mockFetch(body = []) {
  const calls = [];
  const fetchImpl = async (url, options) => {
    calls.push({ url, options });
    return {
      ok: true,
      status: 200,
      json: async () => body,
      text: async () => "",
    };
  };
  return { fetchImpl, calls };
}

describe("optional Foldex origin access", () => {
  test("declares HTTP and HTTPS hosts as optional only", async () => {
    const manifest = await Bun.file(
      new URL("./manifest.json", import.meta.url),
    ).json();

    expect(manifest.host_permissions).toBeUndefined();
    expect(manifest.optional_host_permissions).toEqual([
      "http://*/*",
      "https://*/*",
    ]);
  });

  test("normalizes the backend URL while retaining a reverse-proxy path", () => {
    expect(
      normalizeBaseUrl(" HTTPS://Foldex.Example:443/app///?ignored=1#ignored "),
    ).toBe("https://foldex.example/app");
    expect(normalizeBaseUrl("http://localhost:9089/")).toBe(
      "http://localhost:9089",
    );
    expect(permissionForBaseUrl("https://foldex.example/app")).toEqual({
      origins: ["https://foldex.example/*"],
    });
    expect(permissionForBaseUrl("http://localhost:9089")).toEqual({
      origins: ["http://localhost:9089/*"],
    });
    expect(() => normalizeBaseUrl("ftp://foldex.example")).toThrow(
      "HTTP or HTTPS",
    );
    expect(() =>
      normalizeBaseUrl("https://user:secret@foldex.example"),
    ).toThrow("credentials");
  });

  test("Save requests only the entered origin before storing normalized settings", async () => {
    const { chromeApi, calls } = mockChrome();

    const saved = await saveOptions(
      {
        baseUrl: "https://Foldex.Example:443/app/",
        apiToken: " fx_token ",
        sharedSecret: " old-secret ",
      },
      { chromeApi },
    );

    expect(calls.requests).toEqual([{ origins: ["https://foldex.example/*"] }]);
    expect(calls.writes).toEqual([
      {
        baseUrl: "https://foldex.example/app",
        apiToken: "fx_token",
        sharedSecret: "old-secret",
      },
    ]);
    expect(calls.events).toEqual(["request", "set"]);
    expect(saved.baseUrl).toBe("https://foldex.example/app");
  });

  test("Save denial is actionable and does not persist unusable settings", async () => {
    const { chromeApi, calls } = mockChrome({ requestGranted: false });

    await expect(
      saveOptions(
        { baseUrl: "https://foldex.example", apiToken: "", sharedSecret: "" },
        { chromeApi },
      ),
    ).rejects.toThrow(
      "Access to https://foldex.example was not granted. Choose Allow when prompted, then try again.",
    );
    expect(calls.writes).toEqual([]);
  });

  test("storage write errors reject instead of reporting settings as saved", async () => {
    const { chromeApi, calls } = mockChrome({
      storageSetError: "storage write failed",
    });

    await expect(
      saveOptions(
        {
          baseUrl: "https://foldex.example",
          apiToken: "fx_token",
          sharedSecret: "",
        },
        { chromeApi },
      ),
    ).rejects.toThrow("storage write failed");
    expect(calls.events).toEqual(["request", "set"]);
  });

  test("storage read errors reject so popup startup can render the failure", async () => {
    const { chromeApi, calls } = mockChrome({
      storageGetError: "storage read failed",
    });

    await expect(getStoredConfig(chromeApi)).rejects.toThrow(
      "storage read failed",
    );
    expect(calls.events).toEqual(["get"]);
  });

  test("options UI reports a storage write failure and never reports Saved", async () => {
    const { chromeApi } = mockChrome({
      storageSetError: "storage write failed",
    });
    const { documentApi, elements } = fakeDocument([
      "status",
      "baseUrl",
      "apiToken",
      "sharedSecret",
      "save",
      "test",
    ]);
    const page = initOptionsPage({ documentApi, chromeApi });
    await page.ready;
    elements.baseUrl.value = "https://foldex.example";
    elements.apiToken.value = "fx_token";

    await elements.save.dispatch("click");
    expect(elements.status.textContent).toBe("Not saved: storage write failed");
    expect(elements.status.className).toBe("status error");
    expect(elements.status.textContent).not.toContain("Saved.");
  });

  test("popup startup keeps Save disabled and renders storage read failures", async () => {
    const { chromeApi } = mockChrome({
      storageGetError: "storage read failed",
    });
    const { fetchImpl } = mockFetch();
    const { documentApi, elements } = fakeDocument([
      "tags",
      "status",
      "save",
      "url",
      "title",
      "description",
      "openOptions",
    ]);

    const popup = initPopup({ documentApi, chromeApi, fetchImpl });
    await popup.ready;
    expect(elements.save.disabled).toBe(true);
    expect(elements.status.textContent).toBe(
      "Could not load settings: storage read failed",
    );
    expect(elements.status.className).toBe("status error");
  });

  for (const modifier of ["ctrlKey", "metaKey"]) {
    test(`${modifier === "ctrlKey" ? "Ctrl" : "Meta"}+Enter saves from the popup`, async () => {
      const { chromeApi } = mockChrome();
      const fetchCalls = [];
      const fetchImpl = async (url, options) => {
        fetchCalls.push({ url, options });
        if (url.endsWith("/api/links")) throw new Error("stop after request");
        return {
          ok: true,
          status: 200,
          json: async () => [],
          text: async () => "",
        };
      };
      const { documentApi, elements } = fakeDocument([
        "tags",
        "status",
        "save",
        "url",
        "title",
        "description",
        "openOptions",
      ]);
      const popup = initPopup({ documentApi, chromeApi, fetchImpl });
      await popup.ready;
      elements.url.value = "https://example.com";
      elements.title.value = "Example";
      let prevented = false;

      documentApi.dispatch("keydown", {
        key: "Enter",
        ctrlKey: modifier === "ctrlKey",
        metaKey: modifier === "metaKey",
        preventDefault() {
          prevented = true;
        },
      });
      await Bun.sleep(0);

      expect(prevented).toBe(true);
      expect(
        fetchCalls.filter(({ url }) => url.endsWith("/api/links")),
      ).toHaveLength(1);
    });
  }

  test("options startup renders storage read failures", async () => {
    const { chromeApi } = mockChrome({
      storageGetError: "storage read failed",
    });
    const { documentApi, elements } = fakeDocument([
      "status",
      "baseUrl",
      "apiToken",
      "sharedSecret",
      "save",
      "test",
    ]);

    const page = initOptionsPage({ documentApi, chromeApi });
    await page.ready;
    expect(elements.status.textContent).toBe(
      "Could not load settings: storage read failed",
    );
    expect(elements.status.className).toBe("status error");
  });

  test("Test requests the exact origin and probes only the normalized base URL", async () => {
    const { chromeApi, calls } = mockChrome();
    const { fetchImpl, calls: fetchCalls } = mockFetch([{ id: 1 }]);

    const count = await testConnection(
      {
        baseUrl: "https://foldex.example/app/",
        apiToken: "fx_token",
        sharedSecret: "",
      },
      { chromeApi, fetchImpl },
    );

    expect(count).toBe(1);
    expect(calls.requests).toEqual([{ origins: ["https://foldex.example/*"] }]);
    expect(fetchCalls).toEqual([
      {
        url: "https://foldex.example/app/api/tags",
        options: {
          headers: { Authorization: "Bearer fx_token" },
          redirect: "error",
        },
      },
    ]);
  });

  test("Test denial reports how to retry without making a request", async () => {
    const { chromeApi } = mockChrome({ requestGranted: false });
    const { fetchImpl, calls: fetchCalls } = mockFetch();

    await expect(
      testConnection(
        { baseUrl: "http://localhost:9089", apiToken: "", sharedSecret: "" },
        { chromeApi, fetchImpl },
      ),
    ).rejects.toThrow("Choose Allow when prompted, then try again.");
    expect(fetchCalls).toEqual([]);
  });

  test("popup checks the configured origin before loading and never prompts on open", async () => {
    const { chromeApi, calls } = mockChrome({ containsGranted: false });
    const { fetchImpl, calls: fetchCalls } = mockFetch();

    await expect(
      loadTags(
        {
          baseUrl: "https://foldex.example/app",
          apiToken: "",
          sharedSecret: "",
        },
        { chromeApi, fetchImpl },
      ),
    ).rejects.toThrow("Choose Allow when prompted");

    expect(calls.contains).toEqual([{ origins: ["https://foldex.example/*"] }]);
    expect(calls.requests).toEqual([]);
    expect(fetchCalls).toEqual([]);
  });

  test("popup Save requests and posts only to the configured origin", async () => {
    const { chromeApi, calls } = mockChrome();
    const { fetchImpl, calls: fetchCalls } = mockFetch();

    await saveLink(
      {
        baseUrl: "https://foldex.example/app/",
        apiToken: "fx_token",
        sharedSecret: "",
      },
      {
        url: "https://example.com",
        title: "Example",
        description: null,
        tag_ids: [2],
      },
      { chromeApi, fetchImpl },
    );

    expect(calls.requests).toEqual([{ origins: ["https://foldex.example/*"] }]);
    expect(fetchCalls[0].url).toBe("https://foldex.example/app/api/links");
    expect(fetchCalls[0].options).toMatchObject({
      method: "POST",
      redirect: "error",
    });
  });
});
