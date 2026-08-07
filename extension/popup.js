const $ = (id) => document.getElementById(id);
const tagsEl = $('tags');
const statusEl = $('status');
const saveBtn = $('save');
const selected = new Set();

async function getConfig() {
  return new Promise((resolve) => {
    chrome.storage.local.get(
      { baseUrl: 'http://localhost:9089', apiToken: '', sharedSecret: '' },
      resolve,
    );
  });
}

/**
 * Builds the request headers.
 *
 * The API token is the credential that identifies an account; SHARED_SECRET is
 * a perimeter header that identifies nobody and is on its way out. Both are
 * sent when both are configured, so the backend and the extension can be
 * upgraded in either order without a window where saving a link fails.
 *
 * No CSRF header, and none is needed: a bearer credential is not attached
 * automatically by the browser, so there is no ambient authority for a
 * cross-site request to ride on.
 */
async function authHeaders() {
  const cfg = await getConfig();
  const headers = { 'Content-Type': 'application/json' };
  if (cfg.apiToken) headers['Authorization'] = 'Bearer ' + cfg.apiToken;
  if (cfg.sharedSecret) headers['X-Foldex-Secret'] = cfg.sharedSecret;
  return { cfg, headers };
}

/** Turns a rejected request into advice the popup can act on. */
function credentialProblem(status) {
  if (status === 401) return 'not signed in — set an API token in settings';
  if (status === 403) return 'this token is not allowed here';
  return null;
}

function setStatus(msg, level) {
  statusEl.textContent = msg || '';
  statusEl.className = 'status' + (level ? ' ' + level : '');
}

async function prefill() {
  const [tab] = await chrome.tabs.query({ active: true, currentWindow: true });
  if (!tab) return;
  $('url').value = tab.url || '';
  $('title').value = tab.title || '';
}

async function loadTags() {
  const { cfg, headers } = await authHeaders();
  try {
    const resp = await fetch(cfg.baseUrl + '/api/tags', { headers });
    if (!resp.ok) throw new Error(credentialProblem(resp.status) || 'HTTP ' + resp.status);
    const tags = await resp.json();
    renderTags(tags);
  } catch (e) {
    setStatus('Could not load tags: ' + e.message + ' — check settings', 'error');
  }
}

function renderTags(tags) {
  tagsEl.innerHTML = '';
  for (const t of tags) {
    const chip = document.createElement('span');
    chip.className = 'tag';
    chip.textContent = (t.icon ? t.icon + ' ' : '') + t.name;
    chip.style.borderColor = t.color;
    chip.dataset.id = t.id;
    chip.addEventListener('click', () => {
      const id = Number(chip.dataset.id);
      if (selected.has(id)) {
        selected.delete(id);
        chip.classList.remove('selected');
        chip.style.background = 'rgba(255,255,255,0.03)';
      } else {
        selected.add(id);
        chip.classList.add('selected');
        chip.style.background = t.color;
      }
    });
    tagsEl.appendChild(chip);
  }
}

async function save() {
  const url = $('url').value.trim();
  if (!url) {
    setStatus('URL is required', 'error');
    return;
  }
  saveBtn.disabled = true;
  setStatus('Saving…');
  const { cfg, headers } = await authHeaders();
  try {
    const resp = await fetch(cfg.baseUrl + '/api/links', {
      method: 'POST',
      headers,
      body: JSON.stringify({
        url,
        title: $('title').value.trim() || url,
        description: $('description').value.trim() || null,
        tag_ids: Array.from(selected),
      }),
    });
    if (!resp.ok) {
      const problem = credentialProblem(resp.status);
      if (problem) throw new Error(problem);
      const body = await resp.text();
      throw new Error('HTTP ' + resp.status + ' ' + body.slice(0, 120));
    }
    setStatus('Saved ✓', 'ok');
    setTimeout(() => window.close(), 600);
  } catch (e) {
    setStatus('Save failed: ' + e.message, 'error');
    saveBtn.disabled = false;
  }
}

$('save').addEventListener('click', save);
$('openOptions').addEventListener('click', (e) => {
  e.preventDefault();
  chrome.runtime.openOptionsPage();
});

document.addEventListener('keydown', (e) => {
  if (e.metaKey && e.key === 'Enter') save();
});

(async () => {
  await prefill();
  await loadTags();
})();
