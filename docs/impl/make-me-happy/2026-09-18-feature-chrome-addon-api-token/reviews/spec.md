# Spec review — SDD-CHROME-ADDON-AND-API-TOKEN vs implementation

Both trees reviewed: foldex repo (2e96fc0..HEAD) and the standalone sibling foldex-addon/ (sanctioned mid-run move per user decision; SDD premise amended accordingly).

| Req | Status |
|---|---|
| R1 cap=1 + Rotate (revoke→create same name, SecretBand one-time display, mid-pair failure copy, 409 preserved) | SATISFIED |
| R2.1 manifest (activeTab/storage/scripting/alarms, optional_host_permissions, no <all_urls>, ⌘⇧S command, fx icons, fonts, version lockstep 3.0.10 — amended) | SATISFIED |
| R2.2 two panels per design-reference.html (page card, title, segmented capture + states, folder grid + ＋Nova pasta + 6 swatches, tags Enter/pending + hints, note, save footer; panel B: server/token/test card conta-links-pastas + 401/no-response, 3 capture prefs, default folder, chrome.storage.local footer) | SATISFIED |
| R2.3 capture visible + scroll/stitch (documented sticky/lazy limitations; link→image order; upload failure → "sem imagem" fallback) | SATISFIED |
| R2.4 storage {server, token, prefs, defaultFolderId} (+syncedTags — sanctioned deviation recorded in TASKS.json) | SATISFIED |
| R2.5 _execute_action shortcut | SATISFIED |
| R3 admin download (GET+HEAD, embed go:embed, 503 addon_not_built placeholder, Content-Disposition/X-Addon-Version, admin card + install steps + i18n pt/en/es, Makefile/CI/README) | SATISFIED |
| R4 security (https-only off loopback INV-093, bearer-only headers, admin-only download, per-origin runtime permission request) | SATISFIED |
| §4 existing API contracts untouched | SATISFIED |
| §5 design tokens in popup.css (palette, radii 18·11·999·12, card shadow, focus ring, fx-spin/fx-pop) | SATISFIED |
| §6 tests (backend cap/concurrency/routes, web rotate/card, addon node --test 58, CI freshness gate) | SATISFIED |

Scope creep: none unsanctioned. Sanctioned deviations: standalone addon repo, version lockstep, rotate button, scroll+stitch over debugger, 600px popup cap, `/api` (not the mock's `/api/v1`) copy, options.html retained, es locale (parity gate).

VERDICT: APPROVE
