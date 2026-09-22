import { describe, test } from "node:test";
import assert from "node:assert/strict";

import {
  createFolder,
  createLink,
  listFolders,
  listIdentities,
  listTags,
  statsSummary,
  uploadLinkImage,
} from "../api.js";
import { jsonResponse, mockChrome, mockFetch } from "./helpers.mjs";

const SETTINGS = { server: "https://foldex.example/app", token: "fx_token" };

describe("read endpoints", () => {
  test("GET folders/tags/summary/identities hit the normalized origin with Bearer and never prompt", async () => {
    const { chromeApi, calls } = mockChrome();
    const { fetchImpl, calls: fetchCalls } = mockFetch((url) => {
      if (url.endsWith("/api/folders")) return jsonResponse([{ id: 1, name: "IA" }]);
      if (url.endsWith("/api/tags")) return jsonResponse([{ id: 2, name: "git" }]);
      if (url.endsWith("/api/stats/summary")) return jsonResponse({ total_links: 62 });
      if (url.endsWith("/api/auth/identities")) return jsonResponse([{ id: 9 }]);
      return jsonResponse({});
    });

    const folders = await listFolders(SETTINGS, { chromeApi, fetchImpl });
    const tags = await listTags(SETTINGS, { chromeApi, fetchImpl });
    const summary = await statsSummary(SETTINGS, { chromeApi, fetchImpl });
    const identities = await listIdentities(SETTINGS, { chromeApi, fetchImpl });

    assert.deepEqual(folders, [{ id: 1, name: "IA" }]);
    assert.deepEqual(tags, [{ id: 2, name: "git" }]);
    assert.deepEqual(summary, { total_links: 62 });
    assert.deepEqual(identities, [{ id: 9 }]);
    assert.equal(calls.requests.length, 0);
    assert.equal(calls.contains.length, 4);
    for (const call of fetchCalls) {
      assert.ok(call.url.startsWith("https://foldex.example/app/api/"));
      assert.deepEqual(call.options.headers, { Authorization: "Bearer fx_token" });
      assert.equal(call.options.redirect, "error");
      assert.equal(call.options.method, undefined);
    }
  });

  test("reads without granted origin fail fast with an actionable message and zero fetches", async () => {
    const { chromeApi, calls } = mockChrome({ containsGranted: false });
    const { fetchImpl, calls: fetchCalls } = mockFetch(() => jsonResponse({}));

    await assert.rejects(
      listTags({ server: "https://foldex.example", token: "" }, { chromeApi, fetchImpl }),
      /Choose Allow when prompted/,
    );
    assert.deepEqual(calls.contains, [{ origins: ["https://foldex.example/*"] }]);
    assert.equal(fetchCalls.length, 0);
  });
});

describe("write endpoints", () => {
  test("createFolder posts {name,color} JSON and returns the created folder", async () => {
    const { chromeApi, calls } = mockChrome();
    const { fetchImpl, calls: fetchCalls } = mockFetch(() =>
      jsonResponse({ id: 12, name: "Nova", color: "#10B981" }),
    );

    const folder = await createFolder(
      SETTINGS,
      { name: "Nova", color: "#10B981" },
      { chromeApi, fetchImpl },
    );

    assert.deepEqual(folder, { id: 12, name: "Nova", color: "#10B981" });
    assert.equal(fetchCalls[0].url, "https://foldex.example/app/api/folders");
    assert.equal(fetchCalls[0].options.method, "POST");
    assert.deepEqual(fetchCalls[0].options.headers, {
      Authorization: "Bearer fx_token",
      "Content-Type": "application/json",
    });
    assert.deepEqual(JSON.parse(fetchCalls[0].options.body), { name: "Nova", color: "#10B981" });
    assert.deepEqual(calls.requests, [{ origins: ["https://foldex.example/*"] }]);
  });

  test("createLink posts the full DTO and returns the parsed link for the image upload", async () => {
    const { chromeApi } = mockChrome();
    const { fetchImpl, calls: fetchCalls } = mockFetch(() => jsonResponse({ id: 77 }));

    const link = await createLink(
      SETTINGS,
      {
        url: "https://example.com",
        title: "Example",
        description: "nota",
        folder_id: 12,
        tag_ids: [2],
        pending_tags: ["nova"],
      },
      { chromeApi, fetchImpl },
    );

    assert.equal(link.id, 77);
    assert.equal(fetchCalls[0].url, "https://foldex.example/app/api/links");
    assert.deepEqual(JSON.parse(fetchCalls[0].options.body), {
      url: "https://example.com",
      title: "Example",
      description: "nota",
      folder_id: 12,
      tag_ids: [2],
      pending_tags: ["nova"],
    });
  });

  test("uploadLinkImage posts multipart to /api/links/{id}/image without a JSON content-type", async () => {
    const { chromeApi } = mockChrome();
    const { fetchImpl, calls: fetchCalls } = mockFetch(() => jsonResponse({ ok: true }));
    const png = new Blob([new Uint8Array([137, 80, 78, 71])], { type: "image/png" });

    await uploadLinkImage(SETTINGS, 77, png, { chromeApi, fetchImpl });

    assert.equal(fetchCalls[0].url, "https://foldex.example/app/api/links/77/image");
    assert.equal(fetchCalls[0].options.method, "POST");
    assert.deepEqual(fetchCalls[0].options.headers, { Authorization: "Bearer fx_token" });
    assert.ok(fetchCalls[0].options.body instanceof FormData);
    assert.equal(fetchCalls[0].options.body.get("file").type, "image/png");
  });
});

describe("error mapping (ported from the pre-rework popup contract)", () => {
  test("non-2xx surfaces the envelope's message instead of raw JSON", async () => {
    const { chromeApi } = mockChrome();
    const { fetchImpl } = mockFetch(() =>
      jsonResponse({ error: { code: "url_taken", message: "url already bookmarked" } }, { status: 409 }),
    );

    await assert.rejects(
      createLink(SETTINGS, { url: "https://example.com" }, { chromeApi, fetchImpl }),
      (error) => {
        assert.equal(error.message, "url already bookmarked");
        assert.equal(error.status, 409);
        return true;
      },
    );
  });

  test("non-JSON bodies keep a bounded HTTP fallback and the status rides on the error", async () => {
    const { chromeApi } = mockChrome();
    const { fetchImpl } = mockFetch(() => jsonResponse("x".repeat(500), { status: 502 }));

    await assert.rejects(
      createLink(SETTINGS, { url: "https://example.com" }, { chromeApi, fetchImpl }),
      (error) => {
        assert.match(error.message, /^HTTP 502 .{0,120}$/);
        assert.equal(error.status, 502);
        return true;
      },
    );
  });

  test("401/403 keep the shared credential wording from config.js", async () => {
    const { chromeApi } = mockChrome();
    const { fetchImpl } = mockFetch(() => jsonResponse({}, { status: 401 }));

    await assert.rejects(
      listTags(SETTINGS, { chromeApi, fetchImpl }),
      (error) => {
        assert.match(error.message, /token|signed in/i);
        assert.equal(error.status, 401);
        return true;
      },
    );
  });
});
