# Spec summary — Chrome Addon + API Token

**Spec:** `docs/SDD-CHROME-ADDON-AND-API-TOKEN.md` (CONFIRMED 2026-09-18) · **Kind:** feature · **Branch:** `feature/chrome-addon-api-token`

## Goal

Four gaps closed on top of existing API-token infra (no rework of what exists): (1) one active token per account (`maxTokensPerUser` 20→1) with a **Rotacionar** button (revoke + create same name, plaintext shown once in `SecretBand`, mid-pair failure leaves consistent state); (2) the Chrome MV3 addon in `extension/` — no bundler, vanilla ES modules, 404×632 two-panel popup (Novo link / Configurações), viewport + full-page capture via scroll+stitch (no debugger banner); (3) admin download route serving a `go:embed`-ed deterministic zip behind `RequireAdmin` + `RejectAPIToken`, plus an **Extensão Chrome** card on the admin page; (4) security posture: Bearer only to the configured origin (https in prod, http only for localhost/`.local`), no `<all_urls>` — `optional_host_permissions` + runtime request.

## Actors

- **Usuário (conta web)** — rotates the single token in Perfil → Tokens; **Admin** — downloads the addon zip, sees version + install steps.
- **Addon popup (panels A/B)** — saves the active tab as a link (title/note/folder/tags/capture image), tests the connection, persists `{server, token, prefs, defaultFolderId}` in `chrome.storage.local`.
- **Service worker** — hourly tag-sync alarm (gated by `prefs.sync`).
- **Backend** — enforces cap=1 under the existing `FOR NO KEY UPDATE` lock; serves the embedded zip.

## Contracts (all existing except the new download route)

`POST /api/links {url,title,description,folder_id,tag_ids,pending_tags}` → then `POST /api/links/{id}/image` (multipart `file=png`; upload failure does NOT undo the link — meta "sem imagem") · `POST /api/folders {name,color}` · `GET /api/tags`, `GET /api/folders`, `GET /api/stats/summary` (`summary.total_links`), `GET /api/auth/identities` (first identity or omit line) · `GET/POST /api/auth/tokens`, `DELETE /api/auth/tokens/{id}` (409 `too_many_tokens` when cap hit) · **NEW** `GET /api/admin/addon/download` → 200 zip + `Content-Disposition: attachment; filename="foldex-extension-<version>.zip"` + `X-Addon-Version`, or 503 `addon_not_built` when only the placeholder is embedded. Full examples in `payloads.json`.

## Out of scope (SDD §3 — stays out)

Chrome Web Store publishing · full-page capture via `chrome.debugger` · context menu "Salvar no Foldex" · multi-token · cross-origin iframe capture · no changes to the existing API contracts above.

## Test & gates

Backend: cap=1 cases (0 active → create ok; 1 active → 409; revoke→create; concurrency under lock) + route tests. Web (vitest): rotate flow (success / mid-pair fail / 409), addon card (href, version, i18n pt/en). Extension: `node --test` on pure modules (api client, storage, stitch math, reducers), ≥90% of touched code, red→green per task.

## Planning notes (derived from SDD; flagged for review)

1. **`extension/` is not empty** — a minimal tracked addon already exists (16 files, CI-covered). wt3 reworks it toward the SDD design rather than creating from scratch; existing tests keep passing or are adapted.
2. **`alarms` permission** — SDD R2.2-B5 requires an hourly alarm; the SDD manifest block omits `alarms`. T-012 adds it (only way the requirement is implementable in MV3).
3. **i18n mechanism** — SDD gives pt copy but no mechanism; the repo has `_locales` (en/es/pt) + a CI parity gate. Kept: popup copy via `_locales`, manifest fields per SDD literal (name `foldex`, `version 1.0.0`, `+scripting`, `commands`). Release bump (`v3.1.0`) will move the version via the existing `make release-minor` sync.
4. **Version on the admin card** — no version JSON endpoint exists in the spec; the card reads `X-Addon-Version` via a HEAD to the download route (T-003 supports HEAD, no new endpoint invented).
