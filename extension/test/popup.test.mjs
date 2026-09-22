import { describe, test } from "node:test";
import assert from "node:assert/strict";

import { createPopupController, FOLDER_COLORS } from "../popup.js";
import { jsonResponse, mockChrome, mockFetch } from "./helpers.mjs";

const ALL_IDS = [
  "panelA", "panelB", "connPill", "connPillText", "openSettings",
  "pageChip", "pageTitle", "pageUrl", "linkTitle",
  "modeVisible", "modeFull", "shotSpinner", "shotCaption", "shotImage", "recapture",
  "folderHint", "folderGrid", "newFolderForm", "newFolderName", "swatches",
  "createFolderBtn", "cancelNewFolder",
  "tagHint", "tagInput", "tagChips", "note",
  "savedBanner", "savedLabel", "savedMeta", "newLinkBtn", "saveBtn", "serverMeta",
  "backBtn", "serverInput", "tokenInput", "tokenToggle",
  "testBtn", "connOkCard", "latencyLabel", "statAccount", "statLinks", "statFolders",
  "connErrCard", "connErrTitle", "connErrHint",
  "prefAuto", "prefClose", "prefSync", "defaultFolderChips",
  "saveSettingsBtn", "versionMeta",
];

function fakeElement(tagName = "div") {
  const classes = new Set();
  const listeners = new Map();
  const element = {
    tagName,
    value: "",
    hidden: false,
    disabled: false,
    type: "",
    src: "",
    children: [],
    style: {},
    dataset: {},
    parentElement: null,
    _text: "",
    get textContent() {
      // Real DOM textContent aggregates descendants — folder/tag chips carry
      // their labels in child spans.
      return this._text + this.children.map((child) => child.textContent).join("");
    },
    set textContent(value) {
      this._text = value;
      this.children = [];
    },
    classList: {
      toggle(name, force) {
        const on = force === undefined ? !classes.has(name) : force;
        on ? classes.add(name) : classes.delete(name);
        return on;
      },
      add(name) { classes.add(name); },
      remove(name) { classes.delete(name); },
      contains(name) { return classes.has(name); },
    },
    appendChild(child) {
      child.parentElement = element;
      element.children.push(child);
      return child;
    },
    replaceChildren(...children) {
      element.children = children;
      for (const child of children) child.parentElement = element;
    },
    addEventListener(type, listener) {
      listeners.set(type, listener);
    },
    dispatch(type, event = { preventDefault() {} }) {
      return listeners.get(type)?.(event);
    },
    focus() {},
  };
  element.parentElement = fakePlain();
  return element;
}

function fakePlain() {
  return { hidden: false, textContent: "" };
}

function fakeDocument() {
  const elements = Object.fromEntries(ALL_IDS.map((id) => [id, fakeElement()]));
  return {
    elements,
    documentApi: {
      getElementById(id) {
        return elements[id];
      },
      createElement(tag) {
        return fakeElement(tag);
      },
    },
  };
}

const TAB = {
  id: 5,
  url: "https://docs.anthropic.com/skills/reference",
  title: "Anthropic Docs — Agent Skills reference",
};

const STORED = {
  server: "https://foldex.example",
  token: "fx_token",
  prefs: { auto: true, close: false, sync: false },
  defaultFolderId: 1,
};

function apiRouter(calls = []) {
  return (url, options) => {
    calls.push({ url, options });
    if (url.endsWith("/api/folders") && options?.method !== "POST")
      return jsonResponse([
        { id: 1, name: "IA", color: "#10B981", link_count: 7 },
        { id: 2, name: "Gits", color: "#38BDF8", link_count: 11 },
      ]);
    if (url.endsWith("/api/folders"))
      return jsonResponse({ id: 99, name: "Nova", color: "#38BDF8", link_count: 0 });
    if (url.endsWith("/api/tags"))
      return jsonResponse([{ id: 1, name: "IA" }, { id: 2, name: "git" }]);
    if (url.endsWith("/api/stats/summary")) return jsonResponse({ total_links: 62 });
    if (url.endsWith("/api/auth/identities")) return jsonResponse({ identities: [] });
    if (/\/api\/links\/\d+\/image$/.test(url)) return jsonResponse({ ok: true });
    if (url.endsWith("/api/links")) return jsonResponse({ id: 77 });
    return jsonResponse({});
  };
}

async function boot(overrides = {}) {
  const calls = [];
  const { chromeApi } = mockChrome({
    stored: overrides.stored ?? STORED,
    tabs: overrides.tabs ?? [TAB],
  });
  const { fetchImpl } = mockFetch(overrides.fetch ?? apiRouter(calls));
  const { elements, documentApi } = fakeDocument();
  const closings = [];
  const controller = createPopupController({
    documentApi,
    chromeApi,
    fetchImpl,
    closeFn: () => closings.push(Date.now()),
    delayFn: (ms, fn) => fn(),
    dataUrlToBlob: async () => new Blob(["png"], { type: "image/png" }),
  });
  await controller.ready;
  return { controller, elements, chromeApi, fetchCalls: calls, closings };
}

describe("popup controller", () => {
  test("ready prefills tab, folders, tags, defaults and the meta lines", async () => {
    const { elements } = await boot();

    assert.equal(elements.pageTitle.textContent, TAB.title);
    assert.equal(elements.pageUrl.textContent, "docs.anthropic.com");
    assert.equal(elements.pageChip.textContent, "D");
    assert.equal(elements.linkTitle.value, TAB.title);
    assert.equal(elements.serverMeta.textContent, "foldex.example · v1.0.0");
    assert.equal(elements.versionMeta.textContent, "v1.0.0");
    assert.equal(elements.serverInput.value, "https://foldex.example");
    assert.equal(elements.connPillText.textContent, "conectado");
    assert.equal(elements.folderHint.textContent, "7 links");
    assert.equal(elements.tagHint.textContent, "nenhuma");
    // grid: ＋ Nova pasta, then folder chips with the default (id 1) active
    assert.equal(elements.folderGrid.children.length, 3);
    assert.equal(elements.folderGrid.children[1].textContent, "IA7");
    assert.ok(elements.folderGrid.children[1].classList.contains("active"));
    assert.ok(elements.prefAuto.classList.contains("on"));
    assert.ok(!elements.prefSync.classList.contains("on"));
  });

  test("panels switch via gear/back and the offline pill opens settings", async () => {
    const failing = async () => {
      throw new TypeError("network down");
    };
    const { elements } = await boot({ fetch: failing });

    assert.equal(elements.connPillText.textContent, "sem conexão");
    assert.ok(elements.connPill.classList.contains("off"));
    assert.equal(elements.panelB.hidden, true);

    elements.openSettings.dispatch("click");
    assert.equal(elements.panelA.hidden, true);
    assert.equal(elements.panelB.hidden, false);

    elements.backBtn.dispatch("click");
    assert.equal(elements.panelA.hidden, false);
    assert.equal(elements.panelB.hidden, true);

    elements.connPill.dispatch("click");
    assert.equal(elements.panelB.hidden, false);
  });

  test("Enter adds a pending tag chip; save splits tag_ids vs pending_tags", async () => {
    const { controller, elements, fetchCalls } = await boot();

    elements.tagInput.value = "ClaudeCode";
    elements.tagInput.dispatch("keydown", { key: "Enter", preventDefault() {} });
    assert.equal(elements.tagInput.value, "");
    assert.equal(elements.tagHint.textContent, "1 selecionadas");
    assert.equal(elements.tagChips.children.length, 3); // IA, git + ClaudeCode

    elements.tagChips.children[0].dispatch("click"); // toggle existing 'IA'
    assert.equal(elements.tagHint.textContent, "2 selecionadas");

    await controller.save();
    const linkPost = fetchCalls.find(
      (call) => call.url.endsWith("/api/links") && call.options?.method === "POST",
    );
    assert.deepEqual(JSON.parse(linkPost.options.body), {
      url: TAB.url,
      title: TAB.title,
      description: null,
      folder_id: 1,
      tag_ids: [1],
      pending_tags: ["ClaudeCode"],
    });
  });

  test("save posts the link, uploads the image, banners success and honors close", async () => {
    const { controller, elements, fetchCalls, closings } = await boot({
      stored: { ...STORED, prefs: { auto: false, close: true, sync: false } },
    });
    controller.state.shotBlob = new Blob(["png"], { type: "image/png" });
    controller.state.selectedTags = ["IA"];

    await controller.save();

    const postUrls = fetchCalls
      .filter((call) => call.options?.method === "POST")
      .map((call) => call.url);
    assert.deepEqual(postUrls, [
      "https://foldex.example/api/links",
      "https://foldex.example/api/links/77/image",
    ]);
    assert.equal(elements.savedBanner.hidden, false);
    assert.equal(elements.savedLabel.textContent, "Salvo em IA");
    assert.equal(elements.savedMeta.textContent, "1 tags · imagem anexada");
    assert.equal(elements.saveBtn.hidden, true);
    assert.equal(closings.length, 1);
  });

  test("a failed image upload still succeeds with the sem-imagem meta", async () => {
    const calls = [];
    const router = (url, options) => {
      calls.push({ url, options });
      if (/\/api\/links\/\d+\/image$/.test(url))
        return jsonResponse("boom", { status: 500 });
      return apiRouter()(url, options);
    };
    const { controller, elements } = await boot({
      // auto-capture is off so the test's injected shotBlob cannot race the
      // capture cleanup that a failing captureVisibleTab would trigger.
      stored: { ...STORED, prefs: { auto: false, close: false, sync: false } },
      fetch: router,
    });
    controller.state.shotBlob = new Blob(["png"], { type: "image/png" });

    await controller.save();

    assert.equal(elements.savedBanner.hidden, false);
    assert.equal(elements.savedLabel.textContent, "Salvo em IA");
    assert.equal(elements.savedMeta.textContent, "0 tags · sem imagem");
    assert.equal(elements.saveBtn.hidden, true);
  });

  test("connection test shows latency + stats, 401 and network failures map to cards", async () => {
    const okRouter = (url) => {
      if (url.endsWith("/api/stats/summary")) return jsonResponse({ total_links: 62 });
      if (url.endsWith("/api/auth/identities"))
        return jsonResponse({
          identities: [{ provider: "google", email_at_link: "valmir@x.test" }],
        });
      if (url.endsWith("/api/folders"))
        return jsonResponse([{ id: 1 }, { id: 2 }]);
      return jsonResponse({});
    };
    const { controller, elements } = await boot({ fetch: okRouter });

    await controller.runConnectionTest();
    assert.equal(elements.connOkCard.hidden, false);
    assert.equal(elements.connErrCard.hidden, true);
    assert.equal(elements.latencyLabel.textContent.endsWith(" ms"), true);
    assert.equal(elements.statAccount.textContent, "valmir@x.test");
    assert.equal(elements.statAccount.parentElement.hidden, false);
    assert.equal(elements.statLinks.textContent, "62");
    assert.equal(elements.statFolders.textContent, "2");
    assert.equal(elements.connPillText.textContent, "conectado");

    const rejected = async () => jsonResponse({}, { status: 401 });
    const offline = await boot({ fetch: rejected });
    await offline.controller.runConnectionTest();
    assert.equal(offline.elements.connOkCard.hidden, true);
    assert.equal(offline.elements.connErrCard.hidden, false);
    assert.equal(offline.elements.connErrTitle.textContent, "Token recusado (401)");

    const dead = async () => {
      throw new TypeError("net::ERR_CONNECTION_REFUSED");
    };
    const noServer = await boot({ fetch: dead });
    await noServer.controller.runConnectionTest();
    assert.equal(noServer.elements.connErrTitle.textContent, "Servidor não respondeu");
  });

  test("Nova pasta posts {name,color}, selects the folder and refreshes hints", async () => {
    const { elements, fetchCalls } = await boot();

    elements.folderGrid.children[0].dispatch("click"); // ＋ Nova pasta
    assert.equal(elements.newFolderForm.hidden, false);
    assert.equal(elements.swatches.children.length, FOLDER_COLORS.length);

    elements.swatches.children[1].dispatch("click"); // #38BDF8
    elements.newFolderName.value = "Nova";
    elements.createFolderBtn.dispatch("click");
    await new Promise((resolve) => setTimeout(resolve, 0));

    const folderPost = fetchCalls.find(
      (call) => call.url.endsWith("/api/folders") && call.options?.method === "POST",
    );
    assert.deepEqual(JSON.parse(folderPost.options.body), { name: "Nova", color: "#38BDF8" });
    assert.equal(elements.newFolderForm.hidden, true);
    assert.equal(elements.folderHint.textContent, "0 links");
    assert.equal(elements.folderGrid.children.length, 4);
    assert.ok(elements.folderGrid.children[3].classList.contains("active"));
  });
});
