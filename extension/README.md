# foldex (browser extension)

Vanilla Manifest V3 extension — no bundler. Load directly as "unpacked".

## Install (Chrome / Edge)

1. Open `chrome://extensions` (or `edge://extensions`).
2. Enable **Developer mode**.
3. Click **Load unpacked** and pick this `extension/` folder.
4. Click the puzzle icon and pin **foldex**.
5. Open the popup (icon or **⌘⇧S / Ctrl+Shift+S**), press **⚙** to open the
   settings panel, set the server address (default `http://localhost:9089`) and
   paste an **API token**.
6. Press **Salvar configurações** or **Testar conexão**, then choose **Allow**
   when the browser asks for access to that Foldex server.

## Getting an API token

In Foldex, go to **Profile → Tokens**, give it a name ("Browser extension") and
copy the value. It is shown **once** — the server keeps only a hash, so it
genuinely cannot show it again.

The extension needs a token rather than a session because it has no cookie jar
shared with the app: a popup on `chrome-extension://` is a different origin, and
a refresh token that rotates would be useless to something that may not run for
months.

**What a token can do:** read and write your links and notes. **What it cannot
do:** change your password, list or revoke your sessions, invite anyone,
administer users, or download a backup. Those endpoints refuse bearer tokens
outright. Revoke the token from the same screen and it stops working
immediately.

## The popup (two panels)

- **Novo link** — active page card, editable title, site image
  (**Área visível** via `captureVisibleTab`, **Página inteira** via
  scroll+stitch on an offscreen canvas), folder grid with inline
  **＋ Nova pasta** (six palette swatches → `POST /api/folders`), tags
  (Enter creates a new one → `pending_tags` on save), optional note, and
  **Salvar no Foldex** (`POST /api/links` → `POST /api/links/{id}/image`;
  an image failure still saves with "sem imagem").
- **Configurações (⚙)** — server address, API token (👁 toggle), connection
  test card (latency + account/links/folders), capture defaults, default
  folder. Settings persist to `chrome.storage.local`
  (`{server, token, prefs, defaultFolderId}` — nothing else).
- `options.html` remains as the full-window fallback; it shares the same
  modules as panel B.

Full-page capture limitations (documented, non-blocking): `position:sticky`
elements repeat at each scroll step; lazy images only load when scrolled into
view.

## Server access permission

foldex does not request permanent access to every website at installation.
**Salvar** and **Testar conexão** request optional access only to the origin of
the configured server, e.g. `https://foldex.example/*`. The URL is normalized
before it is saved (INV-093: plain `http://` is allowed only for loopback
hosts); reverse-proxy paths are retained, while query strings, fragments, and
trailing slashes are removed. URLs with embedded credentials are rejected.

If you deny the prompt, settings are not saved. Click the same button again and
choose **Allow**. Reads verify the granted origin first and never prompt on
popup open.

## Fonts and icons

`fonts/` bundles Outfit 400–800 and JetBrains Mono 400–700 (latin subsets,
OFL-licensed, fetched from Fontsource). The CSS declares fallback stacks
(`system-ui` / `ui-monospace`) so the popup degrades gracefully without them.
Icons are the "fx" wordmark — see `icons/README.md` to regenerate.

## Development

- No build step — edit the ES modules directly (`popup.js`, `api.js`,
  `storage.js`, `capture.js`, `state.js`, `background.js`, `config.js`), then
  click the **reload** icon on the extension card.
- Tests: `cd extension && node --test test/` (pure modules; `bun test` also
  runs everything, including the legacy bun suites in the repo root).
- UI copy lives in `_locales/{en,pt,es}/messages.json` — keys must stay in
  parity across the three locales.
