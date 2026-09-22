import { testConnection } from "./api.js";
import { requestOriginAccess } from "./config.js";
import { t } from "./i18n.js";
import { loadSettings, saveSettings } from "./storage.js";

// The popup's Panel B is the primary settings surface (SDD R2.2); this page
// remains as the full-window fallback and shares the same modules.
export function initOptionsPage({
  documentApi = document,
  chromeApi = chrome,
  fetchImpl = fetch,
} = {}) {
  const $ = (id) => documentApi.getElementById(id);
  const statusEl = $("status");
  const T = (key, substitutions) => t(chromeApi, key, substitutions);

  function setStatus(msg, level) {
    statusEl.textContent = msg || "";
    statusEl.className = "status" + (level ? " " + level : "");
  }

  const readInputs = () => ({
    server: $("server").value,
    token: $("token").value,
  });

  const ready = loadSettings(chromeApi)
    .then((settings) => {
      $("server").value = settings.server;
      $("token").value = settings.token;
    })
    .catch((error) => setStatus(T("settingsLoadFailed", [error.message]), "error"));

  async function save() {
    setStatus(T("requestingAccess"));
    try {
      const candidate = readInputs();
      await requestOriginAccess(candidate.server, chromeApi);
      await saveSettings(candidate, chromeApi);
      setStatus(T("savedOk"), "ok");
    } catch (error) {
      setStatus(T("notSaved", [error.message]), "error");
    }
  }

  async function testCurrentConnection() {
    setStatus(T("testing"));
    try {
      const result = await testConnection(readInputs(), { chromeApi, fetchImpl });
      setStatus(T("connected", [String(result.totalLinks ?? 0)]), "ok");
    } catch (error) {
      setStatus(
        error.status
          ? T("tokenRejected", [String(error.status)])
          : T("failed", [error.message]),
        "error",
      );
    }
  }

  $("save").addEventListener("click", () => void save());
  $("test").addEventListener("click", () => void testCurrentConnection());
  return { ready, save, testCurrentConnection };
}

if (typeof document !== "undefined" && typeof chrome !== "undefined") {
  initOptionsPage();
}
