# TASKS — feature: chrome-addon-api-token

Spec: `docs/SDD-CHROME-ADDON-AND-API-TOKEN.md` (CONFIRMED 2026-09-18)

- [ ] T-001 Backend: maxTokensPerUser 20 → 1 with adapted cap tests | tests: 2 | red-green: yes | immutability: no | wt: 1
- [ ] T-002 backend/internal/addon package: go:embed dist zip+version with placeholder semantics | tests: 3 | red-green: yes | immutability: no | wt: 1
- [ ] T-003 GET /api/admin/addon/download route (admin-only, RejectAPIToken, 503 addon_not_built, HEAD support) | tests: 4 | red-green: yes | immutability: no | wt: 1
- [ ] T-004 make extension + test-extension targets, build dependency, CI wiring, README addon section | tests: 2 | red-green: yes | immutability: no | wt: 1
- [ ] T-005 Web: rotateToken helper in api/tokens.ts (revoke → create same name) + unit tests | tests: 3 | red-green: yes | immutability: no | wt: 2
- [ ] T-006 ApiTokensSection: Rotacionar button with success / mid-pair failure / 409 notices | tests: 4 | red-green: yes | immutability: no | wt: 2
- [ ] T-007 Admin page: card Extensão Chrome (download, version, install steps, permissions note, i18n pt/en) | tests: 4 | red-green: yes | immutability: no | wt: 2
- [ ] T-008 Extension: storage layer (server/token/prefs/defaultFolderId defaults + normalization + origin rules) | tests: 4 | red-green: yes | immutability: no | wt: 3
- [ ] T-009 Extension: api client module (URL join, Bearer headers, error mapping, all SDD §4 endpoints) | tests: 6 | red-green: yes | immutability: no | wt: 3
- [ ] T-010 Extension: capture module — visibleTab + scroll/stitch math (steps, offsets/rows, dpr, canvas compose) | tests: 4 | red-green: yes | immutability: no | wt: 3
- [ ] T-011 Extension: folder/tag state reducers (chips toggle, tag_ids vs pending_tags, hints) | tests: 4 | red-green: yes | immutability: no | wt: 3
- [ ] T-012 Extension: manifest to SDD (+scripting, +alarms, commands ⌘⇧S, icons fx-16/32/48/128, fonts woff2) + 2-panel popup structure | tests: 3 | red-green: yes | immutability: no | wt: 3
- [ ] T-013 Extension: popup controller — panel A/B switching, save flow (link → image → success, sem-imagem fallback), connection test card | tests: 5 | red-green: yes | immutability: no | wt: 3
- [ ] T-014 Extension: service worker — hourly tag-sync alarm gated by prefs.sync | tests: 3 | red-green: yes | immutability: no | wt: 3
