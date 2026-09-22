# Tasks — feature

- [x] **T-001** Backend: maxTokensPerUser 20 → 1 with adapted cap tests  | tests: 2 | red-green: yes | immutability: no | wt: 1 | DONE
- [x] **T-002** backend/internal/addon package: go:embed dist zip+version with placeholder semantics  | tests: 3 | red-green: yes | immutability: no | wt: 1 | DONE
- [x] **T-003** GET /api/admin/addon/download route (admin-only, RejectAPIToken, 503 addon_not_built, HEAD support)  | tests: 4 | red-green: yes | immutability: no | wt: 1 | DONE
- [x] **T-004** make extension + test-extension targets, build dependency, CI wiring, README addon section  | tests: 2 | red-green: yes | immutability: no | wt: 1 | DONE
- [x] **T-005** Web: rotateToken helper in api/tokens.ts (revoke → create same name) + unit tests  | tests: 3 | red-green: yes | immutability: no | wt: 2 | DONE
- [x] **T-006** ApiTokensSection: Rotacionar button with success / mid-pair failure / 409 notices  | tests: 4 | red-green: yes | immutability: no | wt: 2 | DONE
- [x] **T-007** Admin page: card Extensão Chrome (download, version, install steps, permissions note, i18n pt/en)  | tests: 4 | red-green: yes | immutability: no | wt: 2 | DONE
- [x] **T-008** Extension: storage layer (server/token/prefs/defaultFolderId defaults + normalization + origin rules)  | tests: 8 | red-green: yes | immutability: no | wt: 3 | DONE
- [x] **T-009** Extension: api client module (URL join, Bearer headers, error mapping, all SDD §4 endpoints)  | tests: 9 | red-green: yes | immutability: no | wt: 3 | DONE
- [x] **T-010** Extension: capture module — visibleTab + scroll/stitch math (steps, offsets/rows, dpr, canvas compose)  | tests: 7 | red-green: yes | immutability: no | wt: 3 | DONE
- [x] **T-011** Extension: folder/tag state reducers (chips toggle, tag_ids vs pending_tags, hints)  | tests: 4 | red-green: yes | immutability: no | wt: 3 | DONE
- [x] **T-012** Extension: manifest to SDD (+scripting, +alarms, commands ⌘⇧S, icons fx-16/32/48/128, fonts woff2) + 2-panel popup structure  | tests: 4 | red-green: yes | immutability: no | wt: 3 | DONE
- [x] **T-013** Extension: popup controller — panel A/B switching, save flow (link → image → success, sem-imagem fallback), connection test card  | tests: 10 | red-green: yes | immutability: no | wt: 3 | DONE
- [x] **T-014** Extension: service worker — hourly tag-sync alarm gated by prefs.sync  | tests: 5 | red-green: yes | immutability: no | wt: 3 | DONE
