const $ = (id) => document.getElementById(id);
const statusEl = $('status');

const DEFAULTS = { baseUrl: 'http://localhost:9089', apiToken: '', sharedSecret: '' };

function setStatus(msg, level) {
  statusEl.textContent = msg || '';
  statusEl.className = 'status' + (level ? ' ' + level : '');
}

chrome.storage.local.get(DEFAULTS, (cfg) => {
  $('baseUrl').value = cfg.baseUrl;
  $('apiToken').value = cfg.apiToken;
  $('sharedSecret').value = cfg.sharedSecret;
});

function readBaseUrl() {
  return ($('baseUrl').value || DEFAULTS.baseUrl).trim().replace(/\/$/, '');
}

/**
 * Builds the credentials to send.
 *
 * BOTH are sent when both are configured, and that is deliberate for the
 * transition: SHARED_SECRET is a perimeter header that identifies nobody, while
 * the API token is the real credential. Sending both means the backend and the
 * extension can be upgraded in either order without a window where saving a
 * link stops working.
 */
function authHeaders(token, secret) {
  const headers = {};
  if (token) headers['Authorization'] = 'Bearer ' + token;
  if (secret) headers['X-Foldex-Secret'] = secret;
  return headers;
}

$('save').addEventListener('click', () => {
  chrome.storage.local.set(
    {
      baseUrl: readBaseUrl(),
      apiToken: $('apiToken').value.trim(),
      sharedSecret: $('sharedSecret').value.trim(),
    },
    () => setStatus('Saved.', 'ok'),
  );
});

/**
 * Tests against /api/tags rather than /healthz.
 *
 * /healthz is public — it answers 200 with no credential at all, so a
 * successful probe there proved only that the server was reachable and told an
 * operator with a wrong token that everything was fine. /api/tags needs the
 * credential the extension actually uses.
 */
$('test').addEventListener('click', async () => {
  setStatus('Testing…');
  const baseUrl = readBaseUrl();
  const headers = authHeaders($('apiToken').value.trim(), $('sharedSecret').value.trim());
  try {
    const resp = await fetch(baseUrl + '/api/tags', { headers });
    if (resp.status === 401 || resp.status === 403) {
      throw new Error('the server rejected the token (HTTP ' + resp.status + ')');
    }
    if (!resp.ok) throw new Error('HTTP ' + resp.status);
    const tags = await resp.json();
    setStatus('Connected. ' + tags.length + ' tag(s) visible.', 'ok');
  } catch (e) {
    setStatus('Failed: ' + e.message, 'error');
  }
});
