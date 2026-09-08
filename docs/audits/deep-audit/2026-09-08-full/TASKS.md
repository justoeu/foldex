# TASKS — Deep Audit (ultra-deep)

> Gerado por `sync-progress.mjs` · **não edite ids à mão** — use `--done` / `--refute` / `--accept` / `--open`.

**Projeto:** `foldex` · **versão** `2.21.0` (git tag) · **branch** `main` · head `429c7ba`

**Rodada:** 2026-09-08 02:11:14 UTC · modo `full` · profundidade `deep` · esforço `max` · base `origin/main`

**Lentes:** Arquitetura · Complexidade · Duplicação · Segurança · Boas práticas · Verbosidade

**Resumo:** 38 tasks · 0 resolvidas · 0 em progresso · 32 pendentes · 6 refutadas · 0 aceitas · **16% fechadas** · ⛔ 8 HIGH+ ABERTO

**Revisão das correções (painel Nêmesis · Hígia · Jano):** 0/0 aprovadas

Legenda: `[ ]` pendente · `[~]` em progresso · `[x]` resolvido (com teste) · `[-]` refutado · `[!]` aceito

## Tasks por severidade

### HIGH (0/9 resolvidos)

- [ ] **ARCH-ATL-001** · Arquitetura · HIGH · `backend/internal/auth/handler.go:79` — auth is a god package (HTTP+SQL+crypto+admin+audit) · resp: Atlas · teste: — · **BLOQUEIA PR** · P0
- [ ] **ARCH-ATL-002** · Arquitetura · HIGH · `backend/internal/pkg/crudupdate/crudupdate.go:21` — pkg/crudupdate imports domain package tags · resp: Atlas · teste: — · **BLOQUEIA PR** · P0
- [ ] **CPX-DED-001** · Complexidade · HIGH · `backend/internal/backup/db_snapshot.go:273` — userObjectKeys mixes attacker regex with ownership · resp: Dédalo · teste: — · **BLOQUEIA PR** · P0
- [ ] **CPX-DED-002** · Complexidade · HIGH · `backend/internal/backup/db_restore_staged.go:517` — restore parent BFS silently reparents cycles/dupes · resp: Dédalo · teste: — · **BLOQUEIA PR** · P0
- [ ] **CPX-DED-003** · Complexidade · HIGH · `backend/internal/folders/repository.go:338` — updateOnce boolean soup can skip folder auth · resp: Dédalo · teste: — · **BLOQUEIA PR** · P0
- [ ] **DUP-ECO-001** · Duplicação · HIGH · `backend/internal/auth/handler.go:1224` — Password floor/cap diverged across auth, master, SPA · resp: Eco · teste: — · **BLOQUEIA PR** · P0
- [ ] **DUP-ECO-002** · Duplicação · HIGH · `web/src/auth/types.ts:57` — HasSecondFactor copies still read totp_enabled alone · resp: Eco · teste: — · **BLOQUEIA PR** · P0
- [ ] **DUP-ECO-003** · Duplicação · HIGH · `backend/internal/links/urls.go:13` — http(s) URL gate forked: Host vs ToLower vs prefix · resp: Eco · teste: — · **BLOQUEIA PR** · P0
- [-] **SEC-CER-005** · Segurança · HIGH · `backend/internal/pkg/outboundhttp/transport.go:43` — Preview/screenshot/oEmbed user URL fetch (SSRF) · resp: Cérbero · teste: —
  > refutado: Dial is pinned to resolved IPs with IMDS always blocked; Chromium capture uses strictDialContext. No unguarded http.Get(userURL) in production.

### MEDIUM (0/18 resolvidos)

- [ ] **ARCH-ATL-003** · Arquitetura · MEDIUM · `backend/internal/backup/db_restore_staged.go:10` — backup restore imports notes HTTP package for sanitizer · resp: Atlas · teste: —
- [ ] **ARCH-ATL-004** · Arquitetura · MEDIUM · `web/src/AppWorkspace.ts:5` — App shell types owned by HomeView leaf · resp: Atlas · teste: —
- [ ] **BEST-ATE-001** · Boas práticas · MEDIUM · `backend/internal/preview/fetch_fallback.go:88` — Bot-wall/SSRF fallback keys off error text · resp: Atena · teste: —
- [ ] **BEST-ATE-002** · Boas práticas · MEDIUM · `backend/internal/auth/handler.go:824` — Auth payloads read policy with Background · resp: Atena · teste: —
- [ ] **CPX-DED-004** · Complexidade · MEDIUM · `backend/internal/importer/validate.go:41` — Validate last-write-wins URL→folder conflicts · resp: Dédalo · teste: —
- [ ] **CPX-DED-005** · Complexidade · MEDIUM · `backend/internal/preview/fetcher.go:169` — parseHead Next() inside title swallows tags · resp: Dédalo · teste: —
- [ ] **CPX-DED-006** · Complexidade · MEDIUM · `backend/internal/backup/service.go:225` — listOwnedObjects packs every ZIP budget in one callback · resp: Dédalo · teste: —
- [ ] **CPX-DED-007** · Complexidade · MEDIUM · `backend/internal/preview/worker.go:311` — preview process inverted nil finishAt status machine · resp: Dédalo · teste: —
- [ ] **CPX-DED-008** · Complexidade · MEDIUM · `backend/internal/auth/twofa_handler.go:877` — stepUpSecondFactor Begin without local settle · resp: Dédalo · teste: —
- [ ] **DUP-ECO-004** · Duplicação · MEDIUM · `backend/internal/policy/policy.go:199` — Hostname allowlist copied: A-Z vs lowercase-only · resp: Eco · teste: —
- [ ] **DUP-ECO-005** · Duplicação · MEDIUM · `backend/internal/imageopt/imageopt.go:50` — Image upload admission is a T3 twin that drifted · resp: Eco · teste: —
- [ ] **DUP-ECO-006** · Duplicação · MEDIUM · `backend/internal/folders/dto.go:175` — Hint-must-not-equal-password copied with trim drift · resp: Eco · teste: —
- [-] **SEC-CER-002** · Segurança · MEDIUM · `backend/internal/auth/handler.go:267` — Login CSRF via form or text/plain · resp: Cérbero · teste: —
  > refutado: No simple-content-type path that both parses credentials and Set-Cookies a SameSite=Lax session from a cross-site top-level POST.
- [-] **SEC-CER-006** · Segurança · MEDIUM · `backend/internal/oauthgoogle/oauthgoogle.go:185` — OAuth Google HTTP client (SSRF) · resp: Cérbero · teste: —
  > refutado: Endpoints are Google constants from config, not request input. Operator-settable token URL is explicitly rejected in comments as credential exfiltration.
- [ ] **VERB-LAC-001** · Verbosidade · MEDIUM · `backend/internal/auth/audit.go:190` — Twin IP-normalize aliases over ipblock.Normalize · resp: Lacônio · teste: —
- [ ] **VERB-LAC-002** · Verbosidade · MEDIUM · `backend/internal/folders/password.go:20` — folders.HashPassword/VerifyPassword only delegate to pwhash · resp: Lacônio · teste: —
- [ ] **VERB-LAC-003** · Verbosidade · MEDIUM · `web/src/api/auth.ts:340` — Two error-envelope extractors for the same axios shape · resp: Lacônio · teste: —
- [ ] **VERB-LAC-004** · Verbosidade · MEDIUM · `backend/internal/pkg/jsonopt/jsonopt.go:27` — DecodeOptionalString trim flag is always true · resp: Lacônio · teste: —

### LOW (0/11 resolvidos)

- [ ] **BEST-ATE-003** · Boas práticas · LOW · `web/src/components/TagPicker.tsx:41` — TagPicker page clamp is effect-derived state · resp: Atena · teste: —
- [ ] **BEST-ATE-004** · Boas práticas · LOW · `web/src/api/entries.ts:298` — Optimistic entry patch asserts across the union · resp: Atena · teste: —
- [-] **SEC-CER-001** · Segurança · LOW · `backend/internal/auth/middleware.go:273` — Cookie mutations gated by signed CSRF vs session hash · resp: Cérbero · teste: —
  > refutado: Signed double-submit on the real Authenticate and Optional paths; naive cookie tossing fails against the stored hash.
- [-] **SEC-CER-003** · Segurança · LOW · `backend/internal/auth/handler.go:681` — POST /logout without CSRF middleware · resp: Cérbero · teste: —
  > refutado: SameSite=Lax explicit (not defaulted) withholds the access cookie from cross-site POST.
- [-] **SEC-CER-004** · Segurança · LOW · `backend/internal/redirect/handler.go:42` — GET /go click-log as GET mutation · resp: Cérbero · teste: —
  > refutado: The mutation is the product of following the short URL. Attacker forcing a visit equals sharing the link; no identity or ACL change.
- [ ] **VERB-LAC-005** · Verbosidade · LOW · `web/src/auth/AuthProvider.lifecycle.ts:93` — isCurrentGeneration is request === current · resp: Lacônio · teste: —
- [ ] **VERB-LAC-006** · Verbosidade · LOW · `web/src/auth/types.ts:69` — canMailStepUpCode wraps one boolean field · resp: Lacônio · teste: —
- [ ] **VERB-LAC-007** · Verbosidade · LOW · `backend/internal/pkg/auditctx/auditctx.go:63` — auditctx.SetRequest only forwards to Set · resp: Lacônio · teste: —
- [ ] **VERB-LAC-008** · Verbosidade · LOW · `backend/internal/preview/worker.go:23` — preview re-exports ports queue sentinels · resp: Lacônio · teste: —
- [ ] **VERB-LAC-009** · Verbosidade · LOW · `web/src/lib/url.ts:88` — hostOf comment restates the one-liner · resp: Lacônio · teste: —
- [ ] **VERB-LAC-010** · Verbosidade · LOW · `backend/internal/notes/image_handler.go:18` — Local alias of imageopt.AllowedUploadMIMEs · resp: Lacônio · teste: —

## Ainda aberto

- **ARCH-ATL-001** (HIGH) — auth is a god package (HTTP+SQL+crypto+admin+audit) · plano: Split along already-extracted seams: keep session/login/cookies in auth; move admin_* to internal/admin (users/invites/transfer); move audit_* to internal/audit; move 2FA/TOTP/recovery to internal/auth/twofa (or authfactor). Handler should take narrow ports instead of owning every limiter and provider field.
- **ARCH-ATL-002** (HIGH) — pkg/crudupdate imports domain package tags · plano: Remove SetEntityTags/TagChanges from pkg/crudupdate. Keep Exec/AssertOwned/SetBuilder as the SQL skeleton only. Callers in links/notes.Update already have tags.SetEntityTagsWithPending — use that (CLAUDE.md §7: prefer tags.SetEntityTags over a pkg wrapper).
- **CPX-DED-001** (HIGH) — userObjectKeys mixes attacker regex with ownership · plano: Split collectLinkCandidates (regex+cap), collectNoteMediaKeys, filterOwnedLinkKeys (ownedLinkIDs then add). Keep add() as the only writer to seen. Test: foreign-id key in og_image_url must not appear; two regex hits for one key keep the owned id.
- **CPX-DED-002** (HIGH) — restore parent BFS silently reparents cycles/dupes · plano: Split indexFolders (reject or collapse duplicate IDs explicitly), buildAdjacency, topoAssign, breakCycles (always nil the broken edge). Return (parents, warnings). Test: A↔B cycle → both roots; duplicate IDs → warning, not silent first-wins.
- **CPX-DED-003** (HIGH) — updateOnce boolean soup can skip folder auth · plano: Named steps: authorizePasswordMutation, authorizeHintMutation, loadHintIfPasswordOnly, rejectHintEqualsPassword, rejectParentCycle, execUpdate. Each returns error; updateOnce only sequences them. Table-test the 4-boolean matrix.
- **DUP-ECO-001** (HIGH) — Password floor/cap diverged across auth, master, SPA · plano: Export one PasswordBounds helper next to auth.validatePasswordAgainst (rune min from policy, byte max 72). Call it from settings master Validate and folder password Validate (cap only). SPA: masterPasswordForm + passwordChecks must use passwordGateLen/usePasswordFloor, not literal 8.
- **DUP-ECO-002** (HIGH) — HasSecondFactor copies still read totp_enabled alone · plano: Replace remaining totp_enabled 'has 2FA' reads with hasSecondFactor(user). Keep totp_enabled only for authenticator-specific UI.
- **DUP-ECO-003** (HIGH) — http(s) URL gate forked: Host vs ToLower vs prefix · plano: Make ValidateAbsoluteHTTPURL (or netpolicy) the only write-path predicate. Netscape + metadata + screenshot scheme pre-check must call it. Keep redirect HasPrefix as defense-in-depth or fold into the same helper.
- **ARCH-ATL-003** (MEDIUM) — backup restore imports notes HTTP package for sanitizer · plano: Move the HTML+plaintext pair into pkg/htmlsanitize (e.g. SanitizeAndPlain) or keep notes.SanitizeBody but have backup call htmlsanitize directly. notes Create/Update Normalize should use the same helper so INV-059 stays in one place.
- **ARCH-ATL-004** (MEDIUM) — App shell types owned by HomeView leaf · plano: Move Sort and ViewMode to web/src/lib/viewPrefs.ts (or api/types.ts). HomeView, Topbar, ListView, CompactGrid, MobileOverflowMenu, AppWorkspace, and AppNavigation all import from there.
- **BEST-ATE-001** (MEDIUM) — Bot-wall/SSRF fallback keys off error text · plano: Add sentinels (var ErrSSRF = errors.New("ssrf"); type httpStatusError struct{code int}) at the dialer/Fetch boundary, wrap with %w, and switch shouldRender to errors.Is/As. Keep the prefix only in Error() text for logs.
- **BEST-ATE-002** (MEDIUM) — Auth payloads read policy with Background · plano: Thread ctx into authenticatedPayload/pendingPayload (first param) and pass r.Context() from startChallenge, Me, and the other JSON call sites. Tests can keep context.Background().
- **CPX-DED-004** (MEDIUM) — Validate last-write-wins URL→folder conflicts · plano: Split aggregateItems (counts + []urls + folder→[]url), queryExistingURLs, applyConflicts, sortFolders. For duplicate URLs, increment Conflicts on every folder that listed the URL (or reject dupes up front).
- **CPX-DED-005** (MEDIUM) — parseHead Next() inside title swallows tags · plano: handleStartTag / handleEndTag. Title text: only read z.Text() when the tokenizer is still on title, never z.Next() past a non-text token. Clamp depth at 0. Test the empty-title + following og:image case.
- **CPX-DED-006** (MEDIUM) — listOwnedObjects packs every ZIP budget in one callback · plano: acceptObject(object, prefix) error for shape checks; addToListing(...) error for counters. Compute remaining := max-(database+headroom+listing.bytes) once with an explicit remaining<0 reject. Test underflow and duplicate keys across prefixes.
- **CPX-DED-007** (MEDIUM) — preview process inverted nil finishAt status machine · plano: Return a three-state (finished | needsRelease | abort) from maybeScreenshot. process switches on it. Never use nil for both success and lookup failure. Test: GetPreview error after pending write must release status.
- **CPX-DED-008** (MEDIUM) — stepUpSecondFactor Begin without local settle · plano: tryStepUpProof(uid, user, code) (SecondFactorProof, error) with no HTTP/limiter. stepUpSecondFactor only Begin + map error → 401 + return key. Force callers through a helper that defers settleStepUp.
- **DUP-ECO-004** (MEDIUM) — Hostname allowlist copied: A-Z vs lowercase-only · plano: One validHostname(s, allowUpper bool) in a tiny pkg (or fold then call policy.validDomain from auth after ToLower of the domain part).
- **DUP-ECO-005** (MEDIUM) — Image upload admission is a T3 twin that drifted · plano: Extract ReadUploadedImage(r, max) in imageopt (MaxBytesReader, field 'image', size, MIME, OptimizeForStore). Handlers keep ownership/lease/key policy only.
- **DUP-ECO-006** (MEDIUM) — Hint-must-not-equal-password copied with trim drift · plano: One HintEqualsPassword(hint, password string) bool (trim + EqualFold) in a small shared helper; SPA ports the same trim before compare.
- **VERB-LAC-001** (MEDIUM) — Twin IP-normalize aliases over ipblock.Normalize · plano: Delete NormalizeIP and normalizeAuditIP. Call ipblock.Normalize at the two remaining sites (audit write + blocklist/admin IP). Inline clientIP/callerIP to that call.
- **VERB-LAC-002** (MEDIUM) — folders.HashPassword/VerifyPassword only delegate to pwhash · plano: Delete HashPassword and VerifyPassword. Call pwhash.Hash / pwhash.Verify from folders/repository.go and folders/handler.go.
- **VERB-LAC-003** (MEDIUM) — Two error-envelope extractors for the same axios shape · plano: Delete errorCode and errorStatus. Point auth screens at apiErrorCode/apiErrorStatus (treat undefined like ''/0 at the few === '' / === 0 sites).
- **VERB-LAC-004** (MEDIUM) — DecodeOptionalString trim flag is always true · plano: Drop the trim parameter; always trim+collapse empty. Keep DecodeOptionalStringRaw for password/hint.
- **BEST-ATE-003** (LOW) — TagPicker page clamp is effect-derived state · plano: const safePage = Math.min(page, Math.max(0, totalPages - 1)); slice with safePage. Drop the effect. Optionally setPage(0) inside setSearch/queue only.
- **BEST-ATE-004** (LOW) — Optimistic entry patch asserts across the union · plano: Split the helper (patchLink vs patchNote) or narrow: if (e.kind === 'link') return { ...e, ...patch }; if ('pinned' in patch) return { ...e, pinned: patch.pinned }; return e. Delete the `as Entry`.
- **VERB-LAC-005** (LOW) — isCurrentGeneration is request === current · plano: Inline `request === reloadRequest.current` at the two probeSession branches; delete the helper and its === tests.
- **VERB-LAC-006** (LOW) — canMailStepUpCode wraps one boolean field · plano: Delete canMailStepUpCode. Read `user.email_2fa_enabled === true` at the two call sites.
- **VERB-LAC-007** (LOW) — auditctx.SetRequest only forwards to Set · plano: Delete SetRequest. Replace the 13 handler call sites with auditctx.Set(r.Context(), ...).
- **VERB-LAC-008** (LOW) — preview re-exports ports queue sentinels · plano: Delete the aliases. Use ports.ErrQueueFull / ports.ErrStopped inside preview (tests included).
- **VERB-LAC-009** (LOW) — hostOf comment restates the one-liner · plano: Delete the restating line. Keep at most the caller note, or nothing.
- **VERB-LAC-010** (LOW) — Local alias of imageopt.AllowedUploadMIMEs · plano: Delete the var. Use imageopt.AllowedUploadMIMEs at the MIME check. Same one-line delete in screenshot_handler.go.

## Roadmap de libs / dependências

**Pesquisa:** 0 lib(s) consultada(s) · 0 em dia · **0 atrás do latest stable** (major 0 · minor 0 · patch 0) · 0 achado(s) de deps em aberto

_Toda lib pesquisada está no latest stable._ ✅

> ⚠️ Consultas que falharam: undefined: undefined — trate o número de desatualizados como piso, não como total.

## Log de iterações

- **2026-09-08 02:30:27 UTC** — TASKS.md criado a partir de FINDINGS.json
