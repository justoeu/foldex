import {
  apiUrl,
  getStoredConfig,
  normalizeBaseUrl,
  requestOriginAccess,
  requireOriginAccess,
} from "./config.js";
import { t } from "./i18n.js";

function authHeaders(config, includeContentType = false) {
  const headers = {};
  if (includeContentType) headers["Content-Type"] = "application/json";
  if (config.apiToken) headers.Authorization = "Bearer " + config.apiToken;
  return headers;
}

function credentialProblem(chromeApi, status) {
  if (status === 401) return t(chromeApi, "notSignedIn");
  if (status === 403) return t(chromeApi, "tokenForbidden");
  return null;
}

export async function loadTags(
  config,
  { chromeApi = chrome, fetchImpl = fetch } = {},
) {
  const baseUrl = await requireOriginAccess(config.baseUrl, chromeApi);
  const resp = await fetchImpl(apiUrl(baseUrl, "/api/tags"), {
    headers: authHeaders(config),
    redirect: "error",
  });
  if (!resp.ok)
    throw new Error(
      credentialProblem(chromeApi, resp.status) || "HTTP " + resp.status,
    );
  return resp.json();
}

export async function saveLink(
  config,
  input,
  { chromeApi = chrome, fetchImpl = fetch } = {},
) {
  const baseUrl = await requestOriginAccess(config.baseUrl, chromeApi);
  const resp = await fetchImpl(apiUrl(baseUrl, "/api/links"), {
    method: "POST",
    headers: authHeaders(config, true),
    body: JSON.stringify(input),
    redirect: "error",
  });
  if (!resp.ok) {
    const problem = credentialProblem(chromeApi, resp.status);
    if (problem) throw new Error(problem);
    const body = await resp.text();
    throw new Error("HTTP " + resp.status + " " + body.slice(0, 120));
  }
}

export function initPopup({
  documentApi = document,
  chromeApi = chrome,
  fetchImpl = fetch,
} = {}) {
  const $ = (id) => documentApi.getElementById(id);
  const tagsEl = $("tags");
  const statusEl = $("status");
  const saveBtn = $("save");
  const selected = new Set();
  let config;

  function setStatus(msg, level) {
    statusEl.textContent = msg || "";
    statusEl.className = "status" + (level ? " " + level : "");
  }

  async function prefill() {
    const [tab] = await chromeApi.tabs.query({
      active: true,
      currentWindow: true,
    });
    if (!tab) return;
    $("url").value = tab.url || "";
    $("title").value = tab.title || "";
  }

  function renderTags(tags) {
    tagsEl.innerHTML = "";
    for (const tag of tags) {
      const chip = documentApi.createElement("span");
      chip.className = "tag";
      chip.textContent = (tag.icon ? tag.icon + " " : "") + tag.name;
      chip.style.borderColor = tag.color;
      chip.dataset.id = tag.id;
      chip.addEventListener("click", () => {
        const id = Number(chip.dataset.id);
        if (selected.has(id)) {
          selected.delete(id);
          chip.classList.remove("selected");
          chip.style.background = "rgba(255,255,255,0.03)";
        } else {
          selected.add(id);
          chip.classList.add("selected");
          chip.style.background = tag.color;
        }
      });
      tagsEl.appendChild(chip);
    }
  }

  async function save() {
    if (saveBtn.disabled) return;
    const url = $("url").value.trim();
    if (!url) {
      setStatus(t(chromeApi, "urlRequired"), "error");
      return;
    }

    saveBtn.disabled = true;
    setStatus(t(chromeApi, "saving"));
    try {
      await saveLink(
        config,
        {
          url,
          title: $("title").value.trim() || url,
          description: $("description").value.trim() || null,
          tag_ids: Array.from(selected),
        },
        { chromeApi, fetchImpl },
      );
      setStatus(t(chromeApi, "saved"), "ok");
      setTimeout(() => window.close(), 600);
    } catch (error) {
      setStatus(t(chromeApi, "saveFailed", [error.message]), "error");
      saveBtn.disabled = false;
    }
  }

  saveBtn.disabled = true;
  $("save").addEventListener("click", save);
  $("openOptions").addEventListener("click", (event) => {
    event.preventDefault();
    chromeApi.runtime.openOptionsPage();
  });
  documentApi.addEventListener("keydown", (event) => {
    if ((event.metaKey || event.ctrlKey) && event.key === "Enter") {
      event.preventDefault();
      void save();
    }
  });

  const load = (async () => {
    try {
      const stored = await getStoredConfig(chromeApi);
      config = { ...stored, baseUrl: normalizeBaseUrl(stored.baseUrl) };
      saveBtn.disabled = false;
      try {
        renderTags(await loadTags(config, { chromeApi, fetchImpl }));
      } catch (error) {
        setStatus(t(chromeApi, "tagsLoadFailed", [error.message]), "error");
      }
    } catch (error) {
      setStatus(t(chromeApi, "settingsLoadFailed", [error.message]), "error");
    }
  })();

  const tab = prefill().catch((error) =>
    setStatus(t(chromeApi, "tabReadFailed", [error.message]), "error"),
  );
  return { ready: Promise.all([load, tab]), save };
}

if (typeof document !== "undefined") initPopup();
