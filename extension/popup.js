import {
  createFolder,
  createLink,
  listFolders,
  listTags,
  testConnection,
  uploadLinkImage,
} from "./api.js";
import { captureFullPage, captureVisible, composeStitch } from "./capture.js";
import { requestOriginAccess } from "./config.js";
import { t } from "./i18n.js";
import {
  addTagDraft,
  folderHint,
  selectFolder,
  splitTagSelection,
  tagHint,
  toggleTag,
} from "./state.js";
import { loadSettings, saveSettings } from "./storage.js";

export const FOLDER_COLORS = [
  "#10B981",
  "#38BDF8",
  "#F5900B",
  "#E0286B",
  "#FACC15",
  "#5B54E8",
];

// Let the success confirmation be seen before the popup dies when the
// "close after saving" pref is on.
const CLOSE_AFTER_SAVE_MS = 600;
const SETTINGS_SAVED_FEEDBACK_MS = 1600;

function canvasToBlob(canvas) {
  return new Promise((resolve, reject) =>
    canvas.toBlob((blob) => (blob ? resolve(blob) : reject(new Error("canvas empty"))), "image/png"),
  );
}

export function createPopupController({
  documentApi = document,
  chromeApi = chrome,
  fetchImpl = fetch,
  closeFn = () => window.close(),
  dataUrlToBlob = (dataUrl) => fetch(dataUrl).then((resp) => resp.blob()),
  delayFn = (ms, fn) => setTimeout(fn, ms),
} = {}) {
  const $ = (id) => documentApi.getElementById(id);

  const state = {
    settings: null,
    tab: null,
    folders: [],
    tags: [],
    selectedFolderId: null,
    selectedTags: [],
    pendingChips: [],
    newFolderColor: FOLDER_COLORS[0],
    shotMode: "visible",
    shotBlob: null,
    shotPreviewSrc: null,
    shotDims: null,
    shotBusy: false,
    saving: false,
    saved: false,
    testing: false,
    connected: false,
  };

  const el = {};
  for (const id of [
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
  ]) {
    el[id] = $(id);
  }

  const T = (key, substitutions) => t(chromeApi, key, substitutions);

  function setConnected(connected) {
    state.connected = connected;
    el.connPill.classList.toggle("off", !connected);
    el.connPillText.textContent = T(connected ? "connConnected" : "connOffline");
  }

  function showPanel(panel) {
    el.panelA.hidden = panel !== "A";
    el.panelB.hidden = panel !== "B";
  }

  function makeEl(tag, className, text) {
    const node = documentApi.createElement(tag);
    if (className) node.className = className;
    if (text != null) node.textContent = text;
    return node;
  }

  function renderFolders() {
    el.folderGrid.replaceChildren();
    const newBtn = makeEl("button", "folder-chip new-folder", T("newFolder"));
    newBtn.type = "button";
    newBtn.addEventListener("click", () => {
      el.newFolderForm.hidden = false;
      el.newFolderName.focus?.();
    });
    el.folderGrid.appendChild(newBtn);

    for (const folder of state.folders) {
      const chip = makeEl("button", "folder-chip");
      chip.type = "button";
      chip.classList.toggle("active", folder.id === state.selectedFolderId);
      const dot = makeEl("span", "dot");
      dot.style.background = folder.color;
      chip.appendChild(dot);
      chip.appendChild(makeEl("span", "name", folder.name));
      chip.appendChild(makeEl("span", "count", String(folder.link_count ?? 0)));
      chip.addEventListener("click", () => {
        state.selectedFolderId = selectFolder(state.selectedFolderId, folder.id);
        renderFolders();
        renderFolderHint();
      });
      el.folderGrid.appendChild(chip);
    }
  }

  function renderFolderHint() {
    const selected = state.folders.find((f) => f.id === state.selectedFolderId);
    const count = folderHint(selected);
    el.folderHint.textContent =
      count == null ? T("hintChooseFolder") : T("hintFolderLinks", [String(count)]);
  }

  function renderTagChips() {
    el.tagChips.replaceChildren();
    const names = [...state.tags.map((tag) => tag.name), ...state.pendingChips];
    for (const name of names) {
      const chip = makeEl("button", "chip" + (state.selectedTags.includes(name) ? " active" : ""), name);
      chip.type = "button";
      chip.addEventListener("click", () => {
        state.selectedTags = toggleTag(state.selectedTags, name);
        renderTagChips();
        renderTagHint();
      });
      el.tagChips.appendChild(chip);
    }
  }

  function renderTagHint() {
    const count = tagHint(state.selectedTags);
    el.tagHint.textContent = count ? T("hintTagsSelected", [String(count)]) : T("hintTagsNone");
  }

  function renderDefaultFolderChips() {
    el.defaultFolderChips.replaceChildren();
    for (const folder of state.folders) {
      const chip = makeEl("button", "chip" + (folder.id === state.settings.defaultFolderId ? " active" : ""));
      chip.type = "button";
      const dot = makeEl("span", "dot");
      dot.style.background = folder.color;
      chip.appendChild(dot);
      chip.appendChild(makeEl("span", null, folder.name));
      chip.addEventListener("click", () => {
        state.settings.defaultFolderId =
          state.settings.defaultFolderId === folder.id ? null : folder.id;
        renderDefaultFolderChips();
      });
      el.defaultFolderChips.appendChild(chip);
    }
  }

  function renderSwatches() {
    el.swatches.replaceChildren();
    for (const color of FOLDER_COLORS) {
      const swatch = makeEl("button", "swatch" + (color === state.newFolderColor ? " active" : ""));
      swatch.type = "button";
      swatch.style.background = color;
      swatch.dataset.color = color;
      swatch.addEventListener("click", () => {
        state.newFolderColor = color;
        renderSwatches();
      });
      el.swatches.appendChild(swatch);
    }
  }

  function renderShot() {
    el.shotSpinner.hidden = !state.shotBusy;
    el.shotCaption.hidden = state.shotBusy || Boolean(state.shotPreviewSrc);
    el.shotImage.hidden = state.shotBusy || !state.shotPreviewSrc;
    el.recapture.hidden = state.shotBusy || !state.shotPreviewSrc;
    if (state.shotPreviewSrc) el.shotImage.src = state.shotPreviewSrc;
    if (state.shotDims) {
      el.shotCaption.textContent = T(
        state.shotMode === "full" ? "shotCaptionFull" : "shotCaptionVisible",
        [String(state.shotDims.width), String(state.shotDims.height)],
      );
    }
  }

  function setShotMode(mode) {
    state.shotMode = mode;
    el.modeVisible.classList.toggle("active", mode === "visible");
    el.modeFull.classList.toggle("active", mode === "full");
    void capture();
  }

  async function capture() {
    if (state.shotBusy || !state.tab) return;
    state.shotBusy = true;
    state.shotBlob = null;
    state.shotPreviewSrc = null;
    state.shotDims = null;
    renderShot();
    try {
      if (state.shotMode === "visible") {
        const dataUrl = await captureVisible(chromeApi);
        state.shotBlob = await dataUrlToBlob(dataUrl);
        state.shotPreviewSrc = dataUrl;
      } else {
        const captured = await captureFullPage(state.tab.id, chromeApi);
        const canvas = await composeStitch(captured);
        state.shotBlob = await canvasToBlob(canvas);
        state.shotPreviewSrc = canvas.toDataURL("image/png");
        state.shotDims = { width: captured.plan.canvasWidth, height: captured.plan.canvasHeight };
      }
    } catch {
      // A failed capture never blocks the save — the link goes out without
      // an image (SDD R2.3 order-of-save rule).
      state.shotBlob = null;
      state.shotPreviewSrc = null;
      state.shotDims = null;
    } finally {
      state.shotBusy = false;
      renderShot();
    }
  }

  function setSaving(saving) {
    state.saving = saving;
    el.saveBtn.disabled = saving;
    el.saveBtn.textContent = T(saving ? "savingBtn" : "saveBtn");
  }

  function showSaved(folderName, tagCount, imageAttached) {
    state.saved = true;
    el.savedBanner.hidden = false;
    el.savedLabel.textContent = T("savedIn", [folderName]);
    el.savedMeta.textContent = T(imageAttached ? "savedMetaImage" : "savedMetaNoImage", [
      String(tagCount),
    ]);
    el.saveBtn.hidden = true;
  }

  async function save() {
    if (state.saving || state.saved) return;
    const url = state.tab?.url;
    if (!url || !state.settings) return;

    setSaving(true);
    try {
      const { tag_ids, pending_tags } = splitTagSelection(state.selectedTags, state.tags);
      const link = await createLink(
        state.settings,
        {
          url,
          title: el.linkTitle.value.trim() || url,
          description: el.note.value.trim() || null,
          folder_id: state.selectedFolderId,
          tag_ids,
          pending_tags,
        },
        { chromeApi, fetchImpl },
      );

      let imageAttached = false;
      if (state.shotBlob) {
        try {
          await uploadLinkImage(state.settings, link.id, state.shotBlob, { chromeApi, fetchImpl });
          imageAttached = true;
        } catch {
          // The link exists server-side; a failed upload must not undo it.
        }
      }

      const folder = state.folders.find((f) => f.id === state.selectedFolderId);
      showSaved(folder?.name ?? "—", tag_ids.length + pending_tags.length, imageAttached);
      setConnected(true);
      if (state.settings.prefs.close) {
        delayFn(CLOSE_AFTER_SAVE_MS, closeFn);
      }
    } catch (error) {
      setSaving(false);
      el.saveBtn.textContent = T("saveFailed", [error.message]);
    }
  }

  function resetAfterSave() {
    state.saved = false;
    el.savedBanner.hidden = true;
    el.saveBtn.hidden = false;
    el.tagInput.value = "";
    setSaving(false);
  }

  async function runConnectionTest() {
    if (state.testing) return;
    state.testing = true;
    el.testBtn.disabled = true;
    el.testBtn.textContent = T("testingBtn");
    el.connOkCard.hidden = true;
    el.connErrCard.hidden = true;
    try {
      const result = await testConnection(readSettingsInputs(), { chromeApi, fetchImpl });
      el.latencyLabel.textContent = T("latencyMs", [String(result.latencyMs)]);
      el.statAccount.textContent = result.account ?? "—";
      el.statAccount.parentElement.hidden = !result.account;
      el.statLinks.textContent = String(result.totalLinks ?? "—");
      el.statFolders.textContent = String(result.folderCount ?? "—");
      el.connOkCard.hidden = false;
      setConnected(true);
    } catch (error) {
      el.connErrTitle.textContent =
        error.status === 401 || error.status === 403
          ? T("connErrRejected", [String(error.status)])
          : error.status
            ? error.message
            : T("connErrNoResponse");
      el.connErrCard.hidden = false;
      setConnected(false);
    } finally {
      state.testing = false;
      el.testBtn.disabled = false;
      el.testBtn.textContent = T("testConnectionBtn");
    }
  }

  function readSettingsInputs() {
    return { ...state.settings, server: el.serverInput.value, token: el.tokenInput.value };
  }

  async function persistSettings() {
    try {
      const candidate = readSettingsInputs();
      // Host permission for a new origin is asked here, at the user gesture
      // (SDD R2.1) — never on popup open.
      await requestOriginAccess(candidate.server, chromeApi);
      const saved = await saveSettings(candidate, chromeApi);
      state.settings = saved;
      renderServerMeta();
      el.saveSettingsBtn.textContent = T("settingsSavedBtn");
      el.saveSettingsBtn.classList.add("saved-state-btn");
      delayFn(SETTINGS_SAVED_FEEDBACK_MS, () => {
        el.saveSettingsBtn.textContent = T("saveSettingsBtn");
        el.saveSettingsBtn.classList.remove("saved-state-btn");
      });
    } catch (error) {
      el.saveSettingsBtn.textContent = T("saveFailed", [error.message]);
    }
  }

  async function submitNewFolder() {
    const name = el.newFolderName.value.trim();
    if (!name) return;
    try {
      const folder = await createFolder(
        state.settings,
        { name, color: state.newFolderColor },
        { chromeApi, fetchImpl },
      );
      state.folders = [...state.folders, folder];
      state.selectedFolderId = folder.id;
      el.newFolderForm.hidden = true;
      el.newFolderName.value = "";
      renderFolders();
      renderFolderHint();
      renderDefaultFolderChips();
      setConnected(true);
    } catch (error) {
      el.createFolderBtn.textContent = T("saveFailed", [error.message]);
    }
  }

  function wireEvents() {
    // The visible-area caption needs the captured PNG's real pixel size;
    // full-page dims are known from the stitch plan already.
    el.shotImage.onload = () => {
      if (!state.shotDims && el.shotImage.naturalWidth) {
        state.shotDims = {
          width: el.shotImage.naturalWidth,
          height: el.shotImage.naturalHeight,
        };
        renderShot();
      }
    };

    el.openSettings.addEventListener("click", () => showPanel("B"));
    el.backBtn.addEventListener("click", () => showPanel("A"));
    el.connPill.addEventListener("click", () => showPanel("B"));
    el.modeVisible.addEventListener("click", () => setShotMode("visible"));
    el.modeFull.addEventListener("click", () => setShotMode("full"));
    el.recapture.addEventListener("click", () => void capture());
    el.cancelNewFolder.addEventListener("click", () => {
      el.newFolderForm.hidden = true;
    });
    el.createFolderBtn.addEventListener("click", () => void submitNewFolder());
    el.tagInput.addEventListener("keydown", (event) => {
      if (event.key !== "Enter") return;
      event.preventDefault();
      const next = addTagDraft(
        { draft: el.tagInput.value, selected: state.selectedTags, pending: state.pendingChips },
        state.tags,
      );
      el.tagInput.value = next.draft;
      state.selectedTags = next.selected;
      state.pendingChips = next.pending;
      renderTagChips();
      renderTagHint();
    });
    el.saveBtn.addEventListener("click", () => void save());
    el.newLinkBtn.addEventListener("click", resetAfterSave);
    el.tokenToggle.addEventListener("click", () => {
      const hidden = el.tokenInput.type === "password";
      el.tokenInput.type = hidden ? "text" : "password";
      el.tokenToggle.textContent = hidden ? "🙈" : "👁";
    });
    el.testBtn.addEventListener("click", () => void runConnectionTest());
    for (const [id, key] of [
      ["prefAuto", "auto"],
      ["prefClose", "close"],
      ["prefSync", "sync"],
    ]) {
      el[id].addEventListener("click", () => {
        state.settings.prefs[key] = !state.settings.prefs[key];
        el[id].classList.toggle("on", state.settings.prefs[key]);
      });
    }
    el.saveSettingsBtn.addEventListener("click", () => void persistSettings());
  }

  function renderServerMeta() {
    let host = "";
    try {
      host = new URL(state.settings.server).host;
    } catch {
      host = state.settings.server;
    }
    const version = chromeApi.runtime.getManifest?.().version ?? "";
    el.serverMeta.textContent = host + (version ? " · v" + version : "");
    el.versionMeta.textContent = version ? "v" + version : "";
  }

  function renderSettingsPanel() {
    el.serverInput.value = state.settings.server;
    el.tokenInput.value = state.settings.token;
    for (const [id, key] of [
      ["prefAuto", "auto"],
      ["prefClose", "close"],
      ["prefSync", "sync"],
    ]) {
      el[id].classList.toggle("on", state.settings.prefs[key]);
    }
    renderServerMeta();
  }

  async function prefillActiveTab() {
    const [tab] = await chromeApi.tabs.query({ active: true, currentWindow: true });
    if (!tab) return;
    state.tab = tab;
    el.pageTitle.textContent = tab.title ?? "";
    let host = "";
    try {
      host = new URL(tab.url).host;
    } catch {
      host = tab.url ?? "";
    }
    el.pageUrl.textContent = host;
    el.pageChip.textContent = (host[0] ?? "◈").toUpperCase();
    el.linkTitle.value = tab.title ?? "";
  }

  const ready = (async () => {
    showPanel("A");
    wireEvents();
    renderSwatches();
    state.settings = await loadSettings(chromeApi);
    if (state.settings.defaultFolderId != null) {
      state.selectedFolderId = state.settings.defaultFolderId;
    }
    renderSettingsPanel();

    const loads = await Promise.allSettled([
      prefillActiveTab(),
      listFolders(state.settings, { chromeApi, fetchImpl }),
      listTags(state.settings, { chromeApi, fetchImpl }),
    ]);

    if (loads[1].status === "fulfilled") {
      state.folders = Array.isArray(loads[1].value) ? loads[1].value : [];
      if (!state.folders.some((f) => f.id === state.selectedFolderId)) {
        state.selectedFolderId = null;
      }
      renderFolders();
      renderFolderHint();
      renderDefaultFolderChips();
      setConnected(true);
    } else {
      renderFolderHint();
    }
    if (loads[2].status === "fulfilled") {
      state.tags = Array.isArray(loads[2].value) ? loads[2].value : [];
    }
    renderTagChips();
    renderTagHint();

    if (loads[1].status === "fulfilled" || loads[2].status === "fulfilled") setConnected(true);
    else setConnected(false);

    if (state.settings.prefs.auto) void capture();
  })();

  return {
    ready,
    state,
    save,
    capture,
    runConnectionTest,
    showPanel,
    submitNewFolder,
    persistSettings,
    addTagDraft: (draft) => {
      const next = addTagDraft(
        { draft, selected: state.selectedTags, pending: state.pendingChips },
        state.tags,
      );
      state.selectedTags = next.selected;
      state.pendingChips = next.pending;
      renderTagChips();
      renderTagHint();
      return next;
    },
  };
}

if (typeof document !== "undefined" && typeof chrome !== "undefined") {
  createPopupController();
}
