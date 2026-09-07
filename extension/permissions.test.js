import { describe, expect, test } from "bun:test";
import { readFileSync } from "node:fs";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";

const EN_MESSAGES = JSON.parse(
  readFileSync(
    join(dirname(fileURLToPath(import.meta.url)), "_locales/en/messages.json"),
    "utf8",
  ),
);

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
  const messages = EN_MESSAGES;
  const chromeApi = {
    i18n: {
      getMessage(key, substitutions) {
        const entry = messages[key];
        if (!entry) return "";
        let text = entry.message;
        const list =
          substitutions == null
            ? []
            : Array.isArray(substitutions)
              ? substitutions
              : [substitutions];
        list.forEach((value, i) => {
          text = text.replaceAll("$" + (i + 1), String(value));
        });
        return text;
      },
    },
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

  test("declares only permissions the code actually calls", async () => {
    const manifest = await Bun.file(
      new URL("./manifest.json", import.meta.url),
    ).json();
    const sources = await Promise.all(
      ["popup.js", "options.js", "config.js"].map((name) =>
        Bun.file(new URL(`./${name}`, import.meta.url)).text(),
      ),
    );

    const namespaces = new Set();
    for (const match of sources.join("\n").matchAll(/chromeApi\.(\w+)/g)) {
      namespaces.add(match[1]);
    }

    // runtime is permission-free; popup queries the active tab only after a
    // user gesture, which is exactly what activeTab grants.
    expect(namespaces).toEqual(
      new Set(["runtime", "tabs", "permissions", "storage"]),
    );
    expect(manifest.permissions.sort()).toEqual(["activeTab", "storage"]);
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
      },
      { chromeApi },
    );

    expect(calls.requests).toEqual([{ origins: ["https://foldex.example/*"] }]);
    expect(calls.writes).toEqual([
      {
        baseUrl: "https://foldex.example/app",
        apiToken: "fx_token",
      },
    ]);
    expect(calls.events).toEqual(["request", "set"]);
    expect(saved.baseUrl).toBe("https://foldex.example/app");
  });

  test("Save denial is actionable and does not persist unusable settings", async () => {
    const { chromeApi, calls } = mockChrome({ requestGranted: false });

    await expect(
      saveOptions(
        { baseUrl: "https://foldex.example", apiToken: "" },
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
        { baseUrl: "http://localhost:9089", apiToken: "" },
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

describe("error and auth contract shared across surfaces", () => {
  test("saveLink surfaces the envelope's message instead of raw JSON", async () => {
    const { chromeApi } = mockChrome();
    const fetchImpl = async () => ({
      ok: false,
      status: 409,
      json: async () => ({
        error: { code: "url_taken", message: "url already bookmarked" },
      }),
      text: async () =>
        JSON.stringify({
          error: { code: "url_taken", message: "url already bookmarked" },
        }),
    });

    await expect(
      saveLink(
        { baseUrl: "http://localhost:9089", apiToken: "fx_token" },
        { url: "https://example.com" },
        { chromeApi, fetchImpl },
      ),
    ).rejects.toThrow(/^url already bookmarked$/);
  });

  test("saveLink keeps a bounded HTTP fallback for non-JSON bodies", async () => {
    const { chromeApi } = mockChrome();
    const fetchImpl = async () => ({
      ok: false,
      status: 502,
      json: async () => {
        throw new Error("not json");
      },
      text: async () => "x".repeat(500),
    });

    await expect(
      saveLink(
        { baseUrl: "http://localhost:9089", apiToken: "fx_token" },
        { url: "https://example.com" },
        { chromeApi, fetchImpl },
      ),
    ).rejects.toThrow(/^HTTP 502 .{0,120}$/);
  });

  test("testConnection maps 401/403 with the same wording as the popup", async () => {
    const { chromeApi } = mockChrome();
    const fetch401 = async () => ({
      ok: false,
      status: 401,
      json: async () => ({}),
      text: async () => "",
    });

    await expect(
      testConnection(
        { baseUrl: "http://localhost:9089", apiToken: "bad" },
        { chromeApi, fetchImpl: fetch401 },
      ),
    ).rejects.toThrow("not signed in — set an API token in settings");
  });

  test("authHeaders and credentialProblem live in config.js, one shape for both surfaces", async () => {
    const { authHeaders, credentialProblem } = await import("./config.js");

    expect(authHeaders({ apiToken: "tok" }, true)).toEqual({
      "Content-Type": "application/json",
      Authorization: "Bearer tok",
    });
    expect(authHeaders({ apiToken: "" })).toEqual({});
    expect(credentialProblem(401)).toBe(
      "not signed in — set an API token in settings",
    );
    expect(credentialProblem(403)).toBe("this token is not allowed here");
    expect(credentialProblem(500)).toBe(null);
  });
});
