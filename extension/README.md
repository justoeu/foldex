# Foldex Capture (browser extension)

Vanilla Manifest V3 extension — no bundler needed. Load directly as "unpacked".

## Install (Chrome / Edge)

1. Open `chrome://extensions` (or `edge://extensions`).
2. Enable **Developer mode**.
3. Click **Load unpacked** and pick this `extension/` folder.
4. Click the puzzle icon and pin **Foldex Capture**.
5. Right-click the icon → **Options** → set the backend URL (default `http://localhost:9089`).

## Usage

Click the icon on any tab → URL and title are pre-filled → pick tags → **Save** (or ⌘/Ctrl+Enter).
The popup closes automatically on success and the SPA picks up the new link within ~1s.

## Notes

- The popup loads tags via `GET /api/tags` and POSTs to `/api/links`.
- If you enabled `SHARED_SECRET` on the backend, put the same value in the extension options.
- No build step — edit `popup.js` / `options.js` directly, then click the **reload** icon on the extension card.
- Icons under `icons/` are placeholders; drop your own 16/48/128 PNGs to replace them.
