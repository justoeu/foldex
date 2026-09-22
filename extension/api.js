import {
  apiUrl,
  authHeaders,
  credentialProblem,
  requestOriginAccess,
  requireOriginAccess,
} from "./config.js";

// Latency probe + the three numbers the connection card shows (SDD R2.2-B4):
// account (first linked identity, if any), total links, folder count.
export async function testConnection(
  settings,
  { chromeApi = chrome, fetchImpl = fetch, now = () => performance.now() } = {},
) {
  const started = now();
  await requestOriginAccess(settings.server, chromeApi);
  const [identities, summary, folders] = await Promise.all([
    listIdentities(settings, { chromeApi, fetchImpl }),
    statsSummary(settings, { chromeApi, fetchImpl }),
    listFolders(settings, { chromeApi, fetchImpl }),
  ]);
  const first = Array.isArray(identities) ? identities[0] : null;
  return {
    latencyMs: Math.round(now() - started),
    account: first ? first.email_at_link || first.provider || null : null,
    totalLinks: summary?.total_links ?? null,
    folderCount: Array.isArray(folders) ? folders.length : null,
  };
}

// The backend answers {error:{code,message}} — surface the human message it
// already wrote; the raw slice is only for non-JSON bodies.
async function unwrapError(resp) {
  const problem = credentialProblem(resp.status);
  if (problem) return apiError(problem, resp.status);
  const body = await resp.text();
  try {
    const parsed = JSON.parse(body);
    if (parsed?.error?.message) return apiError(parsed.error.message, resp.status);
  } catch (err) {
    if (!(err instanceof SyntaxError)) throw err;
  }
  return apiError("HTTP " + resp.status + " " + body.slice(0, 120), resp.status);
}

function apiError(message, status) {
  const error = new Error(message);
  error.status = status;
  return error;
}

async function request(settings, path, { method, body, headers }, chromeApi, fetchImpl, access) {
  const baseUrl = await access(settings.server, chromeApi);
  const resp = await fetchImpl(apiUrl(baseUrl, path), {
    method,
    headers,
    body,
    redirect: "error",
  });
  if (!resp.ok) throw await unwrapError(resp);
  return resp.json();
}

function read(settings, path, deps) {
  const { chromeApi, fetchImpl } = deps;
  return request(
    settings,
    path,
    { headers: authHeaders({ apiToken: settings.token }) },
    chromeApi,
    fetchImpl,
    requireOriginAccess,
  );
}

function write(settings, path, payload, deps) {
  const { chromeApi, fetchImpl } = deps;
  const isJson = typeof payload === "string";
  return request(
    settings,
    path,
    {
      method: "POST",
      headers: authHeaders({ apiToken: settings.token }, isJson),
      body: payload,
    },
    chromeApi,
    fetchImpl,
    requestOriginAccess,
  );
}

export function listFolders(settings, { chromeApi = chrome, fetchImpl = fetch } = {}) {
  return read(settings, "/api/folders", { chromeApi, fetchImpl });
}

export function listTags(settings, { chromeApi = chrome, fetchImpl = fetch } = {}) {
  return read(settings, "/api/tags", { chromeApi, fetchImpl });
}

export function statsSummary(settings, { chromeApi = chrome, fetchImpl = fetch } = {}) {
  return read(settings, "/api/stats/summary", { chromeApi, fetchImpl });
}

export function listIdentities(settings, { chromeApi = chrome, fetchImpl = fetch } = {}) {
  // The endpoint wraps the rows: {identities: [...]}, unlike the others.
  return read(settings, "/api/auth/identities", { chromeApi, fetchImpl }).then(
    (body) => body?.identities ?? [],
  );
}

export function createFolder(settings, folder, { chromeApi = chrome, fetchImpl = fetch } = {}) {
  return write(settings, "/api/folders", JSON.stringify(folder), { chromeApi, fetchImpl });
}

export function createLink(settings, link, { chromeApi = chrome, fetchImpl = fetch } = {}) {
  return write(settings, "/api/links", JSON.stringify(link), { chromeApi, fetchImpl });
}

export function uploadLinkImage(
  settings,
  linkId,
  pngBlob,
  { chromeApi = chrome, fetchImpl = fetch } = {},
) {
  const form = new FormData();
  form.append("file", pngBlob, "image.png");
  // No Content-Type: the multipart boundary is the browser's job.
  return write(settings, `/api/links/${linkId}/image`, form, { chromeApi, fetchImpl });
}
