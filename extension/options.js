import {
  apiUrl,
  getStoredConfig,
  normalizeBaseUrl,
  requestOriginAccess,
  setStoredConfig,
} from './config.js';

function authHeaders(token, secret) {
  const headers = {};
  if (token) headers.Authorization = 'Bearer ' + token;
  if (secret) headers['X-Foldex-Secret'] = secret;
  return headers;
}

function normalizeOptions(values) {
  return {
    baseUrl: normalizeBaseUrl(values.baseUrl),
    apiToken: values.apiToken.trim(),
    sharedSecret: values.sharedSecret.trim(),
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

  const resp = await fetchImpl(apiUrl(config.baseUrl, '/api/tags'), {
    headers: authHeaders(config.apiToken, config.sharedSecret),
    redirect: 'error',
  });
  if (resp.status === 401 || resp.status === 403) {
    throw new Error('the server rejected the token (HTTP ' + resp.status + ')');
  }
  if (!resp.ok) throw new Error('HTTP ' + resp.status);
  const tags = await resp.json();
  return tags.length;
}

function initOptionsPage() {
  const $ = (id) => document.getElementById(id);
  const statusEl = $('status');

  function setStatus(msg, level) {
    statusEl.textContent = msg || '';
    statusEl.className = 'status' + (level ? ' ' + level : '');
  }

  function readOptions() {
    return {
      baseUrl: $('baseUrl').value,
      apiToken: $('apiToken').value,
      sharedSecret: $('sharedSecret').value,
    };
  }

  getStoredConfig(chrome)
    .then((config) => {
      $('baseUrl').value = config.baseUrl;
      $('apiToken').value = config.apiToken;
      $('sharedSecret').value = config.sharedSecret;
    })
    .catch((error) => setStatus('Could not load settings: ' + error.message, 'error'));

  $('save').addEventListener('click', async () => {
    setStatus('Requesting access…');
    try {
      const config = await saveOptions(readOptions());
      $('baseUrl').value = config.baseUrl;
      setStatus('Saved.', 'ok');
    } catch (error) {
      setStatus('Not saved: ' + error.message, 'error');
    }
  });

  $('test').addEventListener('click', async () => {
    setStatus('Testing…');
    try {
      const tagCount = await testConnection(readOptions());
      setStatus('Connected. ' + tagCount + ' tag(s) visible.', 'ok');
    } catch (error) {
      setStatus('Failed: ' + error.message, 'error');
    }
  });
}

if (typeof document !== 'undefined') initOptionsPage();
