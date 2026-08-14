import { describe, expect, test } from 'bun:test';

import { normalizeBaseUrl, permissionForBaseUrl } from './config.js';
import { saveOptions, testConnection } from './options.js';
import { loadTags, saveLink } from './popup.js';

function mockChrome({ requestGranted = true, containsGranted = true } = {}) {
  const calls = { requests: [], contains: [], writes: [], events: [] };
  const chromeApi = {
    runtime: {},
    permissions: {
      request(permission, callback) {
        calls.requests.push(permission);
        calls.events.push('request');
        callback(requestGranted);
      },
      contains(permission, callback) {
        calls.contains.push(permission);
        calls.events.push('contains');
        callback(containsGranted);
      },
    },
    storage: {
      local: {
        set(value, callback) {
          calls.writes.push(value);
          calls.events.push('set');
          callback();
        },
      },
    },
  };
  return { chromeApi, calls };
}

function mockFetch(body = []) {
  const calls = [];
  const fetchImpl = async (url, options) => {
    calls.push({ url, options });
    return {
      ok: true,
      status: 200,
      json: async () => body,
      text: async () => '',
    };
  };
  return { fetchImpl, calls };
}

describe('optional Foldex origin access', () => {
  test('declares HTTP and HTTPS hosts as optional only', async () => {
    const manifest = await Bun.file(new URL('./manifest.json', import.meta.url)).json();

    expect(manifest.host_permissions).toBeUndefined();
    expect(manifest.optional_host_permissions).toEqual(['http://*/*', 'https://*/*']);
  });

  test('normalizes the backend URL while retaining a reverse-proxy path', () => {
    expect(normalizeBaseUrl(' HTTPS://Foldex.Example:443/app///?ignored=1#ignored ')).toBe(
      'https://foldex.example/app',
    );
    expect(normalizeBaseUrl('http://localhost:9089/')).toBe('http://localhost:9089');
    expect(permissionForBaseUrl('https://foldex.example/app')).toEqual({
      origins: ['https://foldex.example/*'],
    });
    expect(permissionForBaseUrl('http://localhost:9089')).toEqual({
      origins: ['http://localhost:9089/*'],
    });
    expect(() => normalizeBaseUrl('ftp://foldex.example')).toThrow('HTTP or HTTPS');
    expect(() => normalizeBaseUrl('https://user:secret@foldex.example')).toThrow('credentials');
  });

  test('Save requests only the entered origin before storing normalized settings', async () => {
    const { chromeApi, calls } = mockChrome();

    const saved = await saveOptions(
      {
        baseUrl: 'https://Foldex.Example:443/app/',
        apiToken: ' fx_token ',
        sharedSecret: ' old-secret ',
      },
      { chromeApi },
    );

    expect(calls.requests).toEqual([{ origins: ['https://foldex.example/*'] }]);
    expect(calls.writes).toEqual([
      {
        baseUrl: 'https://foldex.example/app',
        apiToken: 'fx_token',
        sharedSecret: 'old-secret',
      },
    ]);
    expect(calls.events).toEqual(['request', 'set']);
    expect(saved.baseUrl).toBe('https://foldex.example/app');
  });

  test('Save denial is actionable and does not persist unusable settings', async () => {
    const { chromeApi, calls } = mockChrome({ requestGranted: false });

    await expect(
      saveOptions(
        { baseUrl: 'https://foldex.example', apiToken: '', sharedSecret: '' },
        { chromeApi },
      ),
    ).rejects.toThrow(
      'Access to https://foldex.example was not granted. Choose Allow when prompted, then try again.',
    );
    expect(calls.writes).toEqual([]);
  });

  test('Test requests the exact origin and probes only the normalized base URL', async () => {
    const { chromeApi, calls } = mockChrome();
    const { fetchImpl, calls: fetchCalls } = mockFetch([{ id: 1 }]);

    const count = await testConnection(
      {
        baseUrl: 'https://foldex.example/app/',
        apiToken: 'fx_token',
        sharedSecret: '',
      },
      { chromeApi, fetchImpl },
    );

    expect(count).toBe(1);
    expect(calls.requests).toEqual([{ origins: ['https://foldex.example/*'] }]);
    expect(fetchCalls).toEqual([
      {
        url: 'https://foldex.example/app/api/tags',
        options: {
          headers: { Authorization: 'Bearer fx_token' },
          redirect: 'error',
        },
      },
    ]);
  });

  test('Test denial reports how to retry without making a request', async () => {
    const { chromeApi } = mockChrome({ requestGranted: false });
    const { fetchImpl, calls: fetchCalls } = mockFetch();

    await expect(
      testConnection(
        { baseUrl: 'http://localhost:9089', apiToken: '', sharedSecret: '' },
        { chromeApi, fetchImpl },
      ),
    ).rejects.toThrow('Choose Allow when prompted, then try again.');
    expect(fetchCalls).toEqual([]);
  });

  test('popup checks the configured origin before loading and never prompts on open', async () => {
    const { chromeApi, calls } = mockChrome({ containsGranted: false });
    const { fetchImpl, calls: fetchCalls } = mockFetch();

    await expect(
      loadTags(
        { baseUrl: 'https://foldex.example/app', apiToken: '', sharedSecret: '' },
        { chromeApi, fetchImpl },
      ),
    ).rejects.toThrow('Choose Allow when prompted');

    expect(calls.contains).toEqual([{ origins: ['https://foldex.example/*'] }]);
    expect(calls.requests).toEqual([]);
    expect(fetchCalls).toEqual([]);
  });

  test('popup Save requests and posts only to the configured origin', async () => {
    const { chromeApi, calls } = mockChrome();
    const { fetchImpl, calls: fetchCalls } = mockFetch();

    await saveLink(
      { baseUrl: 'https://foldex.example/app/', apiToken: 'fx_token', sharedSecret: '' },
      { url: 'https://example.com', title: 'Example', description: null, tag_ids: [2] },
      { chromeApi, fetchImpl },
    );

    expect(calls.requests).toEqual([{ origins: ['https://foldex.example/*'] }]);
    expect(fetchCalls[0].url).toBe('https://foldex.example/app/api/links');
    expect(fetchCalls[0].options).toMatchObject({ method: 'POST', redirect: 'error' });
  });
});
