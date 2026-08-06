# Foldex Capture (browser extension)

Vanilla Manifest V3 extension — no bundler needed. Load directly as "unpacked".

## Install (Chrome / Edge)

1. Open `chrome://extensions` (or `edge://extensions`).
2. Enable **Developer mode**.
3. Click **Load unpacked** and pick this `extension/` folder.
4. Click the puzzle icon and pin **Foldex Capture**.
5. Right-click the icon → **Options** → set the backend URL (default `http://localhost:9089`)
   and paste an **API token**.

## Getting an API token

In Foldex, go to **Settings → API tokens**, give it a name ("Browser extension") and
copy the value. It is shown **once** — the server keeps only a hash, so it genuinely
cannot show it again. Paste it into the extension options and press **Test connection**.

The extension needs a token rather than a session because it has no cookie jar shared
with the app: a background popup on `chrome-extension://` is a different origin, and a
refresh token that rotates would be useless to something that may not run for months.

**What a token can do:** read and write your links and notes. **What it cannot do:**
change your password, list or revoke your sessions, invite anyone, administer users, or
download a backup. Those endpoints refuse bearer tokens outright, so a token pasted into
a config file is not your account. Revoke one from the same screen and it stops working
immediately.

## Usage

Click the icon on any tab → URL and title are pre-filled → pick tags → **Save** (or ⌘/Ctrl+Enter).
The popup closes automatically on success and the SPA picks up the new link within ~1s.

## Notes

- The popup loads tags via `GET /api/tags` and POSTs to `/api/links`, sending
  `Authorization: Bearer <token>`.
- **`SHARED_SECRET` is deprecated.** The field is still there and still sends
  `X-Foldex-Secret` alongside the token, so the backend and the extension can be
  upgraded in either order. Leave it empty unless your server still sets the variable.
- Getting `not signed in`? The token is missing, revoked, expired, or belongs to a
  disabled account. Mint a new one.
- No build step — edit `popup.js` / `options.js` directly, then click the **reload** icon
  on the extension card.
- Icons under `icons/` are placeholders; drop your own 16/48/128 PNGs to replace them.
