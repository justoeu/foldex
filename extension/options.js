import {
  apiUrl,
  getStoredConfig,
  normalizeBaseUrl,
  requestOriginAccess,
  setStoredConfig,
} from "./config.js";
import { t } from "./i18n.js";

function authHeaders(token) {
  const headers = {};
  if (token) headers.Authorization = "Bearer " + token;
  return headers;
}

function normalizeOptions(values) {
  return {
    baseUrl: normalizeBaseUrl(values.baseUrl),
    apiToken: values.apiToken.trim(),
  };
}

export async function saveOptions(values, { chromeApi = chrome } = {}) {
  const config = normalizeOptions(values);
  await requestOriginAccess(config.baseUrl, chromeApi);
  await setStoredConfig(config, chromeApi);
  return config;
}

export async function testConnection(
  values,
  { chromeApi = chrome, fetchImpl = fetch } = {},
) {
  const config = normalizeOptions(values);
  await requestOriginAccess(config.baseUrl, chromeApi);

  const resp = await fetchImpl(apiUrl(config.baseUrl, "/api/tags"), {
    headers: authHeaders(config.apiToken),
    redirect: "error",
  });
  if (resp.status === 401 || resp.status === 403) {
    throw new Error(t(chromeApi, "tokenRejected", [String(resp.status)]));
  }
  if (!resp.ok) throw new Error("HTTP " + resp.status);
  const tags = await resp.json();
  return tags.length;
}

export function initOptionsPage({
  documentApi = document,
  chromeApi = chrome,
} = {}) {
  const $ = (id) => documentApi.getElementById(id);
  const statusEl = $("status");

  function setStatus(msg, level) {
    statusEl.textContent = msg || "";
    statusEl.className = "status" + (level ? " " + level : "");
  }

  function readOptions() {
    return {
      baseUrl: $("baseUrl").value,
      apiToken: $("apiToken").value,
    };
  }

  const ready = getStoredConfig(chromeApi)
    .then((config) => {
      $("baseUrl").value = config.baseUrl;
      $("apiToken").value = config.apiToken;
    })
    .catch((error) =>
      setStatus(t(chromeApi, "settingsLoadFailed", [error.message]), "error"),
    );

  async function save() {
    setStatus(t(chromeApi, "requestingAccess"));
    try {
      const config = await saveOptions(readOptions(), { chromeApi });
      $("baseUrl").value = config.baseUrl;
      setStatus(t(chromeApi, "savedOk"), "ok");
    } catch (error) {
      setStatus(t(chromeApi, "notSaved", [error.message]), "error");
    }
  }

  async function testCurrentConnection() {
    setStatus(t(chromeApi, "testing"));
    try {
      const tagCount = await testConnection(readOptions(), { chromeApi });
      setStatus(t(chromeApi, "connected", [String(tagCount)]), "ok");
    } catch (error) {
      setStatus(t(chromeApi, "failed", [error.message]), "error");
    }
  }

  $("save").addEventListener("click", save);
  $("test").addEventListener("click", testCurrentConnection);
  return { ready, save, testCurrentConnection };
}

if (typeof document !== "undefined") initOptionsPage();
