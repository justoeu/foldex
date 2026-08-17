export const DEFAULT_CONFIG = Object.freeze({
  baseUrl: 'http://localhost:9089',
  apiToken: '',
});

export function normalizeBaseUrl(rawBaseUrl) {
  if (typeof rawBaseUrl !== 'string') {
    throw new Error('Enter a valid HTTP or HTTPS backend URL.');
  }

  let url;
  try {
    url = new URL(rawBaseUrl.trim() || DEFAULT_CONFIG.baseUrl);
  } catch {
    throw new Error('Enter a valid HTTP or HTTPS backend URL.');
  }

  if (url.protocol !== 'http:' && url.protocol !== 'https:') {
    throw new Error('The backend URL must use HTTP or HTTPS.');
  }
  if (url.username || url.password) {
    throw new Error('The backend URL must not contain credentials.');
  }

  const path = url.pathname.replace(/\/+$/, '');
  return url.origin + (path === '/' ? '' : path);
}

export function permissionForBaseUrl(baseUrl) {
  const origin = new URL(normalizeBaseUrl(baseUrl)).origin;
  return { origins: [origin + '/*'] };
}

export function apiUrl(baseUrl, path) {
  return normalizeBaseUrl(baseUrl) + path;
}

function permissionError(baseUrl) {
  const origin = new URL(normalizeBaseUrl(baseUrl)).origin;
  return new Error(
    'Access to ' + origin + ' was not granted. Choose Allow when prompted, then try again.',
  );
}

function callChrome(chromeApi, target, method, value) {
  return new Promise((resolve, reject) => {
    target[method](value, (result) => {
      const lastError = chromeApi.runtime && chromeApi.runtime.lastError;
      if (lastError) {
        reject(new Error(lastError.message));
        return;
      }
      resolve(result);
    });
  });
}

export async function requestOriginAccess(baseUrl, chromeApi) {
  const granted = await callChrome(
    chromeApi,
    chromeApi.permissions,
    'request',
    permissionForBaseUrl(baseUrl),
  );
  if (!granted) throw permissionError(baseUrl);
  return normalizeBaseUrl(baseUrl);
}

export async function requireOriginAccess(baseUrl, chromeApi) {
  const granted = await callChrome(
    chromeApi,
    chromeApi.permissions,
    'contains',
    permissionForBaseUrl(baseUrl),
  );
  if (!granted) throw permissionError(baseUrl);
  return normalizeBaseUrl(baseUrl);
}

export function getStoredConfig(chromeApi) {
  return callChrome(chromeApi, chromeApi.storage.local, 'get', DEFAULT_CONFIG)
    .then((config) => {
      // Releases before the SHARED_SECRET removal left the key behind; it is
      // inert now — clear it so secret material does not linger in storage.
      // Guarded because test doubles and old Chrome versions may lack remove().
      try {
        if (typeof chromeApi.storage.local.remove === 'function') {
          chromeApi.storage.local.remove('sharedSecret');
        }
      } catch {
        // Cleanup is best-effort; never block config loading on it.
      }
      return config;
    });
}

export function setStoredConfig(config, chromeApi) {
  return callChrome(chromeApi, chromeApi.storage.local, 'set', config);
}
