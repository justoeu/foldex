const $ = (id) => document.getElementById(id);
const statusEl = $('status');

function setStatus(msg, level) {
  statusEl.textContent = msg || '';
  statusEl.className = 'status' + (level ? ' ' + level : '');
}

chrome.storage.local.get({ baseUrl: 'http://localhost:9089', sharedSecret: '' }, (cfg) => {
  $('baseUrl').value = cfg.baseUrl;
  $('sharedSecret').value = cfg.sharedSecret;
});

$('save').addEventListener('click', () => {
  chrome.storage.local.set(
    {
      baseUrl: ($('baseUrl').value || 'http://localhost:9089').trim().replace(/\/$/, ''),
      sharedSecret: $('sharedSecret').value.trim(),
    },
    () => setStatus('Saved.', 'ok'),
  );
});

$('test').addEventListener('click', async () => {
  setStatus('Testing…');
  const baseUrl = ($('baseUrl').value || 'http://localhost:9089').trim().replace(/\/$/, '');
  const headers = {};
  const secret = $('sharedSecret').value.trim();
  if (secret) headers['X-Foldex-Secret'] = secret;
  try {
    const resp = await fetch(baseUrl + '/healthz', { headers });
    if (!resp.ok) throw new Error('HTTP ' + resp.status);
    const body = await resp.json();
    setStatus('Connected: ' + JSON.stringify(body), 'ok');
  } catch (e) {
    setStatus('Failed: ' + e.message, 'error');
  }
});
