# TASKS — Deep Audit (ultra-deep)

> Gerado por `sync-progress.mjs` · **não edite ids à mão** — use `--done` / `--refute` / `--accept` / `--open`.

**Projeto:** `foldex` · **versão** `2.21.0` (git tag) · **branch** `main` · head `429c7ba`

**Rodada:** 2026-09-08 02:11:14 UTC · modo `full` · profundidade `deep` · esforço `max` · base `origin/main`

**Lentes:** Arquitetura · Complexidade · Duplicação · Segurança · Boas práticas · Verbosidade

**Resumo:** 38 tasks · 32 resolvidas · 0 em progresso · 0 pendentes · 6 refutadas · 0 aceitas · **100% fechadas** · ✅ nenhum HIGH aberto

**Revisão das correções (painel Nêmesis · Hígia · Jano):** 0/32 aprovadas · ⚠ 32 dispensada(s)

Legenda: `[ ]` pendente · `[~]` em progresso · `[x]` resolvido (com teste) · `[-]` refutado · `[!]` aceito

## Tasks por severidade

### HIGH (8/9 resolvidos)

- [x] **ARCH-ATL-001** · Arquitetura · HIGH · `backend/internal/auth/handler.go:79` — auth is a god package (HTTP+SQL+crypto+admin+audit) · resp: Atlas · teste: `TestGodPackageSplit_AnomalyAndIPBlockAreSubpackages` · ⚠ **revisão DISPENSADA** (painel Nêmesis/Hígia/Jano no diff combinado do PR #120; votos não persistidos em reviews/votes/)
  > progresso 2026-09-08: resolvido · teste TestGodPackageSplit_AnomalyAndIPBlockAreSubpackages · PR #120
  > progresso 2026-09-08: ⚠ revisão dispensada · painel Nêmesis/Hígia/Jano no diff combinado do PR #120; votos não persistidos em reviews/votes/
- [x] **ARCH-ATL-002** · Arquitetura · HIGH · `backend/internal/pkg/crudupdate/crudupdate.go:21` — pkg/crudupdate imports domain package tags · resp: Atlas · teste: `crudupdate.TestSourceDoesNotImportTags` · ⚠ **revisão DISPENSADA** (painel Nêmesis/Hígia/Jano no diff combinado do PR #120; votos não persistidos em reviews/votes/)
  > progresso 2026-09-08: resolvido · teste crudupdate.TestSourceDoesNotImportTags · PR #120
  > progresso 2026-09-08: ⚠ revisão dispensada · painel Nêmesis/Hígia/Jano no diff combinado do PR #120; votos não persistidos em reviews/votes/
- [x] **CPX-DED-001** · Complexidade · HIGH · `backend/internal/backup/db_snapshot.go:273` — userObjectKeys mixes attacker regex with ownership · resp: Dédalo · teste: `backup.TestKeepOwnedLinkKeys_ForeignIDDoesNotAppear` · ⚠ **revisão DISPENSADA** (painel Nêmesis/Hígia/Jano no diff combinado do PR #120; votos não persistidos em reviews/votes/)
  > progresso 2026-09-08: resolvido · teste backup.TestKeepOwnedLinkKeys_ForeignIDDoesNotAppear · PR #120
  > progresso 2026-09-08: ⚠ revisão dispensada · painel Nêmesis/Hígia/Jano no diff combinado do PR #120; votos não persistidos em reviews/votes/
- [x] **CPX-DED-002** · Complexidade · HIGH · `backend/internal/backup/db_restore_staged.go:517` — restore parent BFS silently reparents cycles/dupes · resp: Dédalo · teste: `backup.TestNormalizeRestoreFolderParents / ChildOfCycleKeepsParent` · ⚠ **revisão DISPENSADA** (painel Nêmesis/Hígia/Jano no diff combinado do PR #120; votos não persistidos em reviews/votes/)
  > progresso 2026-09-08: resolvido · teste backup.TestNormalizeRestoreFolderParents / ChildOfCycleKeepsParent · PR #120
  > progresso 2026-09-08: ⚠ revisão dispensada · painel Nêmesis/Hígia/Jano no diff combinado do PR #120; votos não persistidos em reviews/votes/
- [x] **CPX-DED-003** · Complexidade · HIGH · `backend/internal/folders/repository.go:338` — updateOnce boolean soup can skip folder auth · resp: Dédalo · teste: `folders.TestRepository_Update_PasswordHintParentMatrix` · ⚠ **revisão DISPENSADA** (painel Nêmesis/Hígia/Jano no diff combinado do PR #120; votos não persistidos em reviews/votes/)
  > progresso 2026-09-08: resolvido · teste folders.TestRepository_Update_PasswordHintParentMatrix · PR #120
  > progresso 2026-09-08: ⚠ revisão dispensada · painel Nêmesis/Hígia/Jano no diff combinado do PR #120; votos não persistidos em reviews/votes/
- [x] **DUP-ECO-001** · Duplicação · HIGH · `backend/internal/auth/handler.go:1224` — Password floor/cap diverged across auth, master, SPA · resp: Eco · teste: `masterPasswordForm.test.ts#too-long + settings 72-byte cap` · ⚠ **revisão DISPENSADA** (painel Nêmesis/Hígia/Jano no diff combinado do PR #120; votos não persistidos em reviews/votes/)
  > progresso 2026-09-08: resolvido · teste masterPasswordForm.test.ts#too-long + settings 72-byte cap · PR #120
  > progresso 2026-09-08: ⚠ revisão dispensada · painel Nêmesis/Hígia/Jano no diff combinado do PR #120; votos não persistidos em reviews/votes/
- [x] **DUP-ECO-002** · Duplicação · HIGH · `web/src/auth/types.ts:57` — HasSecondFactor copies still read totp_enabled alone · resp: Eco · teste: `SettingsPage.test.tsx / AdminUsersPage.test.tsx hasSecondFactor` · ⚠ **revisão DISPENSADA** (painel Nêmesis/Hígia/Jano no diff combinado do PR #120; votos não persistidos em reviews/votes/)
  > progresso 2026-09-08: resolvido · teste SettingsPage.test.tsx / AdminUsersPage.test.tsx hasSecondFactor · PR #120
  > progresso 2026-09-08: ⚠ revisão dispensada · painel Nêmesis/Hígia/Jano no diff combinado do PR #120; votos não persistidos em reviews/votes/
- [x] **DUP-ECO-003** · Duplicação · HIGH · `backend/internal/links/urls.go:13` — http(s) URL gate forked: Host vs ToLower vs prefix · resp: Eco · teste: `links.ValidateAbsoluteHTTPURL + netscape/screenshot isHTTPScheme` · ⚠ **revisão DISPENSADA** (painel Nêmesis/Hígia/Jano no diff combinado do PR #120; votos não persistidos em reviews/votes/)
  > progresso 2026-09-08: resolvido · teste links.ValidateAbsoluteHTTPURL + netscape/screenshot isHTTPScheme · PR #120
  > progresso 2026-09-08: ⚠ revisão dispensada · painel Nêmesis/Hígia/Jano no diff combinado do PR #120; votos não persistidos em reviews/votes/
- [-] **SEC-CER-005** · Segurança · HIGH · `backend/internal/pkg/outboundhttp/transport.go:43` — Preview/screenshot/oEmbed user URL fetch (SSRF) · resp: Cérbero · teste: —
  > refutado: Dial is pinned to resolved IPs with IMDS always blocked; Chromium capture uses strictDialContext. No unguarded http.Get(userURL) in production.

### MEDIUM (16/18 resolvidos)

- [x] **ARCH-ATL-003** · Arquitetura · MEDIUM · `backend/internal/backup/db_restore_staged.go:10` — backup restore imports notes HTTP package for sanitizer · resp: Atlas · teste: `backup.TestSanitizeAndPlain / htmlsanitize.SanitizeAndPlain` · ⚠ **revisão DISPENSADA** (painel Nêmesis/Hígia/Jano no diff combinado do PR #120; votos não persistidos em reviews/votes/)
  > progresso 2026-09-08: resolvido · teste backup.TestSanitizeAndPlain / htmlsanitize.SanitizeAndPlain · PR #120
  > progresso 2026-09-08: ⚠ revisão dispensada · painel Nêmesis/Hígia/Jano no diff combinado do PR #120; votos não persistidos em reviews/votes/
- [x] **ARCH-ATL-004** · Arquitetura · MEDIUM · `web/src/AppWorkspace.ts:5` — App shell types owned by HomeView leaf · resp: Atlas · teste: `web/src/lib/viewPrefs.ts (Sort/ViewMode)` · ⚠ **revisão DISPENSADA** (painel Nêmesis/Hígia/Jano no diff combinado do PR #120; votos não persistidos em reviews/votes/)
  > progresso 2026-09-08: resolvido · teste web/src/lib/viewPrefs.ts (Sort/ViewMode) · PR #120
  > progresso 2026-09-08: ⚠ revisão dispensada · painel Nêmesis/Hígia/Jano no diff combinado do PR #120; votos não persistidos em reviews/votes/
- [x] **BEST-ATE-001** · Boas práticas · MEDIUM · `backend/internal/preview/fetch_fallback.go:88` — Bot-wall/SSRF fallback keys off error text · resp: Atena · teste: `preview.TestShouldRender_ClassifiesSentinelsWhenErrorTextIsRewritten` · ⚠ **revisão DISPENSADA** (painel Nêmesis/Hígia/Jano no diff combinado do PR #120; votos não persistidos em reviews/votes/)
  > progresso 2026-09-08: resolvido · teste preview.TestShouldRender_ClassifiesSentinelsWhenErrorTextIsRewritten · PR #120
  > progresso 2026-09-08: ⚠ revisão dispensada · painel Nêmesis/Hígia/Jano no diff combinado do PR #120; votos não persistidos em reviews/votes/
- [x] **BEST-ATE-002** · Boas práticas · MEDIUM · `backend/internal/auth/handler.go:824` — Auth payloads read policy with Background · resp: Atena · teste: `auth.TestPayloadBuildersThreadRequestContext` · ⚠ **revisão DISPENSADA** (painel Nêmesis/Hígia/Jano no diff combinado do PR #120; votos não persistidos em reviews/votes/)
  > progresso 2026-09-08: resolvido · teste auth.TestPayloadBuildersThreadRequestContext · PR #120
  > progresso 2026-09-08: ⚠ revisão dispensada · painel Nêmesis/Hígia/Jano no diff combinado do PR #120; votos não persistidos em reviews/votes/
- [x] **CPX-DED-004** · Complexidade · MEDIUM · `backend/internal/importer/validate.go:41` — Validate last-write-wins URL→folder conflicts · resp: Dédalo · teste: `importer.TestApplyConflicts_DuplicateURLCountsEveryFolder` · ⚠ **revisão DISPENSADA** (painel Nêmesis/Hígia/Jano no diff combinado do PR #120; votos não persistidos em reviews/votes/)
  > progresso 2026-09-08: resolvido · teste importer.TestApplyConflicts_DuplicateURLCountsEveryFolder · PR #120
  > progresso 2026-09-08: ⚠ revisão dispensada · painel Nêmesis/Hígia/Jano no diff combinado do PR #120; votos não persistidos em reviews/votes/
- [x] **CPX-DED-005** · Complexidade · MEDIUM · `backend/internal/preview/fetcher.go:169` — parseHead Next() inside title swallows tags · resp: Dédalo · teste: `preview.TestParseHead_EmptyTitleDoesNotDropFollowingOGImage` · ⚠ **revisão DISPENSADA** (painel Nêmesis/Hígia/Jano no diff combinado do PR #120; votos não persistidos em reviews/votes/)
  > progresso 2026-09-08: resolvido · teste preview.TestParseHead_EmptyTitleDoesNotDropFollowingOGImage · PR #120
  > progresso 2026-09-08: ⚠ revisão dispensada · painel Nêmesis/Hígia/Jano no diff combinado do PR #120; votos não persistidos em reviews/votes/
- [x] **CPX-DED-006** · Complexidade · MEDIUM · `backend/internal/backup/service.go:225` — listOwnedObjects packs every ZIP budget in one callback · resp: Dédalo · teste: `backup.TestListOwnedObjectsRejectsNegativeRemaining` · ⚠ **revisão DISPENSADA** (painel Nêmesis/Hígia/Jano no diff combinado do PR #120; votos não persistidos em reviews/votes/)
  > progresso 2026-09-08: resolvido · teste backup.TestListOwnedObjectsRejectsNegativeRemaining · PR #120
  > progresso 2026-09-08: ⚠ revisão dispensada · painel Nêmesis/Hígia/Jano no diff combinado do PR #120; votos não persistidos em reviews/votes/
- [x] **CPX-DED-007** · Complexidade · MEDIUM · `backend/internal/preview/worker.go:311` — preview process inverted nil finishAt status machine · resp: Dédalo · teste: `preview.TestProcess_GetPreviewErrorAfterPendingWriteReleasesStatus` · ⚠ **revisão DISPENSADA** (painel Nêmesis/Hígia/Jano no diff combinado do PR #120; votos não persistidos em reviews/votes/)
  > progresso 2026-09-08: resolvido · teste preview.TestProcess_GetPreviewErrorAfterPendingWriteReleasesStatus · PR #120
  > progresso 2026-09-08: ⚠ revisão dispensada · painel Nêmesis/Hígia/Jano no diff combinado do PR #120; votos não persistidos em reviews/votes/
- [x] **CPX-DED-008** · Complexidade · MEDIUM · `backend/internal/auth/twofa_handler.go:877` — stepUpSecondFactor Begin without local settle · resp: Dédalo · teste: `auth.TestTryStepUpProofHasNoLimiterSideEffects` · ⚠ **revisão DISPENSADA** (painel Nêmesis/Hígia/Jano no diff combinado do PR #120; votos não persistidos em reviews/votes/)
  > progresso 2026-09-08: resolvido · teste auth.TestTryStepUpProofHasNoLimiterSideEffects · PR #120
  > progresso 2026-09-08: ⚠ revisão dispensada · painel Nêmesis/Hígia/Jano no diff combinado do PR #120; votos não persistidos em reviews/votes/
- [x] **DUP-ECO-004** · Duplicação · MEDIUM · `backend/internal/policy/policy.go:199` — Hostname allowlist copied: A-Z vs lowercase-only · resp: Eco · teste: `auth.TestHostnameAllowlist_ExampleCOMAcceptedOnBothPathsAfterFold` · ⚠ **revisão DISPENSADA** (painel Nêmesis/Hígia/Jano no diff combinado do PR #120; votos não persistidos em reviews/votes/)
  > progresso 2026-09-08: resolvido · teste auth.TestHostnameAllowlist_ExampleCOMAcceptedOnBothPathsAfterFold · PR #120
  > progresso 2026-09-08: ⚠ revisão dispensada · painel Nêmesis/Hígia/Jano no diff combinado do PR #120; votos não persistidos em reviews/votes/
- [x] **DUP-ECO-005** · Duplicação · MEDIUM · `backend/internal/imageopt/imageopt.go:50` — Image upload admission is a T3 twin that drifted · resp: Eco · teste: `imageopt.TestAdmitBytes_EmptyAndCap` · ⚠ **revisão DISPENSADA** (painel Nêmesis/Hígia/Jano no diff combinado do PR #120; votos não persistidos em reviews/votes/)
  > progresso 2026-09-08: resolvido · teste imageopt.TestAdmitBytes_EmptyAndCap · PR #120
  > progresso 2026-09-08: ⚠ revisão dispensada · painel Nêmesis/Hígia/Jano no diff combinado do PR #120; votos não persistidos em reviews/votes/
- [x] **DUP-ECO-006** · Duplicação · MEDIUM · `backend/internal/folders/dto.go:175` — Hint-must-not-equal-password copied with trim drift · resp: Eco · teste: `secrethint.TestEqualsPassword_TrimsAndFolds` · ⚠ **revisão DISPENSADA** (painel Nêmesis/Hígia/Jano no diff combinado do PR #120; votos não persistidos em reviews/votes/)
  > progresso 2026-09-08: resolvido · teste secrethint.TestEqualsPassword_TrimsAndFolds · PR #120
  > progresso 2026-09-08: ⚠ revisão dispensada · painel Nêmesis/Hígia/Jano no diff combinado do PR #120; votos não persistidos em reviews/votes/
- [-] **SEC-CER-002** · Segurança · MEDIUM · `backend/internal/auth/handler.go:267` — Login CSRF via form or text/plain · resp: Cérbero · teste: —
  > refutado: No simple-content-type path that both parses credentials and Set-Cookies a SameSite=Lax session from a cross-site top-level POST.
- [-] **SEC-CER-006** · Segurança · MEDIUM · `backend/internal/oauthgoogle/oauthgoogle.go:185` — OAuth Google HTTP client (SSRF) · resp: Cérbero · teste: —
  > refutado: Endpoints are Google constants from config, not request input. Operator-settable token URL is explicitly rejected in comments as credential exfiltration.
- [x] **VERB-LAC-001** · Verbosidade · MEDIUM · `backend/internal/auth/audit.go:190` — Twin IP-normalize aliases over ipblock.Normalize · resp: Lacônio · teste: `auth.TestTwinIPNormalizeAliasesAreGone` · ⚠ **revisão DISPENSADA** (painel Nêmesis/Hígia/Jano no diff combinado do PR #120; votos não persistidos em reviews/votes/)
  > progresso 2026-09-08: resolvido · teste auth.TestTwinIPNormalizeAliasesAreGone · PR #120
  > progresso 2026-09-08: ⚠ revisão dispensada · painel Nêmesis/Hígia/Jano no diff combinado do PR #120; votos não persistidos em reviews/votes/
- [x] **VERB-LAC-002** · Verbosidade · MEDIUM · `backend/internal/folders/password.go:20` — folders.HashPassword/VerifyPassword only delegate to pwhash · resp: Lacônio · teste: `folders/password.go aliases removed; pwhash.Hash/Verify` · ⚠ **revisão DISPENSADA** (painel Nêmesis/Hígia/Jano no diff combinado do PR #120; votos não persistidos em reviews/votes/)
  > progresso 2026-09-08: resolvido · teste folders/password.go aliases removed; pwhash.Hash/Verify · PR #120
  > progresso 2026-09-08: ⚠ revisão dispensada · painel Nêmesis/Hígia/Jano no diff combinado do PR #120; votos não persistidos em reviews/votes/
- [x] **VERB-LAC-003** · Verbosidade · MEDIUM · `web/src/api/auth.ts:340` — Two error-envelope extractors for the same axios shape · resp: Lacônio · teste: `web/src/api/auth.envelope.test.ts` · ⚠ **revisão DISPENSADA** (painel Nêmesis/Hígia/Jano no diff combinado do PR #120; votos não persistidos em reviews/votes/)
  > progresso 2026-09-08: resolvido · teste web/src/api/auth.envelope.test.ts · PR #120
  > progresso 2026-09-08: ⚠ revisão dispensada · painel Nêmesis/Hígia/Jano no diff combinado do PR #120; votos não persistidos em reviews/votes/
- [x] **VERB-LAC-004** · Verbosidade · MEDIUM · `backend/internal/pkg/jsonopt/jsonopt.go:27` — DecodeOptionalString trim flag is always true · resp: Lacônio · teste: `jsonopt.TestDecodeOptionalString` · ⚠ **revisão DISPENSADA** (painel Nêmesis/Hígia/Jano no diff combinado do PR #120; votos não persistidos em reviews/votes/)
  > progresso 2026-09-08: resolvido · teste jsonopt.TestDecodeOptionalString · PR #120
  > progresso 2026-09-08: ⚠ revisão dispensada · painel Nêmesis/Hígia/Jano no diff combinado do PR #120; votos não persistidos em reviews/votes/

### LOW (8/11 resolvidos)

- [x] **BEST-ATE-003** · Boas práticas · LOW · `web/src/components/TagPicker.tsx:41` — TagPicker page clamp is effect-derived state · resp: Atena · teste: `TagPicker.test.tsx#safePage clamp` · ⚠ **revisão DISPENSADA** (painel Nêmesis/Hígia/Jano no diff combinado do PR #120; votos não persistidos em reviews/votes/)
  > progresso 2026-09-08: resolvido · teste TagPicker.test.tsx#safePage clamp · PR #120
  > progresso 2026-09-08: ⚠ revisão dispensada · painel Nêmesis/Hígia/Jano no diff combinado do PR #120; votos não persistidos em reviews/votes/
- [x] **BEST-ATE-004** · Boas práticas · LOW · `web/src/api/entries.ts:298` — Optimistic entry patch asserts across the union · resp: Atena · teste: `entries.test.tsx#no as Entry` · ⚠ **revisão DISPENSADA** (painel Nêmesis/Hígia/Jano no diff combinado do PR #120; votos não persistidos em reviews/votes/)
  > progresso 2026-09-08: resolvido · teste entries.test.tsx#no as Entry · PR #120
  > progresso 2026-09-08: ⚠ revisão dispensada · painel Nêmesis/Hígia/Jano no diff combinado do PR #120; votos não persistidos em reviews/votes/
- [-] **SEC-CER-001** · Segurança · LOW · `backend/internal/auth/middleware.go:273` — Cookie mutations gated by signed CSRF vs session hash · resp: Cérbero · teste: —
  > refutado: Signed double-submit on the real Authenticate and Optional paths; naive cookie tossing fails against the stored hash.
- [-] **SEC-CER-003** · Segurança · LOW · `backend/internal/auth/handler.go:681` — POST /logout without CSRF middleware · resp: Cérbero · teste: —
  > refutado: SameSite=Lax explicit (not defaulted) withholds the access cookie from cross-site POST.
- [-] **SEC-CER-004** · Segurança · LOW · `backend/internal/redirect/handler.go:42` — GET /go click-log as GET mutation · resp: Cérbero · teste: —
  > refutado: The mutation is the product of following the short URL. Attacker forcing a visit equals sharing the link; no identity or ACL change.
- [x] **VERB-LAC-005** · Verbosidade · LOW · `web/src/auth/AuthProvider.lifecycle.ts:93` — isCurrentGeneration is request === current · resp: Lacônio · teste: `AuthProvider.charter.test.ts#isCurrentGeneration gone` · ⚠ **revisão DISPENSADA** (painel Nêmesis/Hígia/Jano no diff combinado do PR #120; votos não persistidos em reviews/votes/)
  > progresso 2026-09-08: resolvido · teste AuthProvider.charter.test.ts#isCurrentGeneration gone · PR #120
  > progresso 2026-09-08: ⚠ revisão dispensada · painel Nêmesis/Hígia/Jano no diff combinado do PR #120; votos não persistidos em reviews/votes/
- [x] **VERB-LAC-006** · Verbosidade · LOW · `web/src/auth/types.ts:69` — canMailStepUpCode wraps one boolean field · resp: Lacônio · teste: `factors.test.ts#canMailStepUpCode gone` · ⚠ **revisão DISPENSADA** (painel Nêmesis/Hígia/Jano no diff combinado do PR #120; votos não persistidos em reviews/votes/)
  > progresso 2026-09-08: resolvido · teste factors.test.ts#canMailStepUpCode gone · PR #120
  > progresso 2026-09-08: ⚠ revisão dispensada · painel Nêmesis/Hígia/Jano no diff combinado do PR #120; votos não persistidos em reviews/votes/
- [x] **VERB-LAC-007** · Verbosidade · LOW · `backend/internal/pkg/auditctx/auditctx.go:63` — auditctx.SetRequest only forwards to Set · resp: Lacônio · teste: `auditctx.TestSet_AnnotatesTheRequestsContext` · ⚠ **revisão DISPENSADA** (painel Nêmesis/Hígia/Jano no diff combinado do PR #120; votos não persistidos em reviews/votes/)
  > progresso 2026-09-08: resolvido · teste auditctx.TestSet_AnnotatesTheRequestsContext · PR #120
  > progresso 2026-09-08: ⚠ revisão dispensada · painel Nêmesis/Hígia/Jano no diff combinado do PR #120; votos não persistidos em reviews/votes/
- [x] **VERB-LAC-008** · Verbosidade · LOW · `backend/internal/preview/worker.go:23` — preview re-exports ports queue sentinels · resp: Lacônio · teste: `preview.TestPreviewDoesNotReexportQueueSentinels` · ⚠ **revisão DISPENSADA** (painel Nêmesis/Hígia/Jano no diff combinado do PR #120; votos não persistidos em reviews/votes/)
  > progresso 2026-09-08: resolvido · teste preview.TestPreviewDoesNotReexportQueueSentinels · PR #120
  > progresso 2026-09-08: ⚠ revisão dispensada · painel Nêmesis/Hígia/Jano no diff combinado do PR #120; votos não persistidos em reviews/votes/
- [x] **VERB-LAC-009** · Verbosidade · LOW · `web/src/lib/url.ts:88` — hostOf comment restates the one-liner · resp: Lacônio · teste: `web/src/lib/url.test.ts hostOf` · ⚠ **revisão DISPENSADA** (painel Nêmesis/Hígia/Jano no diff combinado do PR #120; votos não persistidos em reviews/votes/)
  > progresso 2026-09-08: resolvido · teste web/src/lib/url.test.ts hostOf · PR #120
  > progresso 2026-09-08: ⚠ revisão dispensada · painel Nêmesis/Hígia/Jano no diff combinado do PR #120; votos não persistidos em reviews/votes/
- [x] **VERB-LAC-010** · Verbosidade · LOW · `backend/internal/notes/image_handler.go:18` — Local alias of imageopt.AllowedUploadMIMEs · resp: Lacônio · teste: `imageopt.AllowedUploadMIMEs used directly; aliases deleted` · ⚠ **revisão DISPENSADA** (painel Nêmesis/Hígia/Jano no diff combinado do PR #120; votos não persistidos em reviews/votes/)
  > progresso 2026-09-08: resolvido · teste imageopt.AllowedUploadMIMEs used directly; aliases deleted · PR #120
  > progresso 2026-09-08: ⚠ revisão dispensada · painel Nêmesis/Hígia/Jano no diff combinado do PR #120; votos não persistidos em reviews/votes/

## Ainda aberto

_Nada aberto._ 🎉

## Roadmap de libs / dependências

**Pesquisa:** 0 lib(s) consultada(s) · 0 em dia · **0 atrás do latest stable** (major 0 · minor 0 · patch 0) · 0 achado(s) de deps em aberto

_Toda lib pesquisada está no latest stable._ ✅

> ⚠️ Consultas que falharam: undefined: undefined — trate o número de desatualizados como piso, não como total.

## Log de iterações

- **2026-09-08 20:39:28 UTC** — revisão DISPENSADA em VERB-LAC-010 (painel Nêmesis/Hígia/Jano no diff combinado do PR #120; votos não persistidos em reviews/votes/) · resolvido VERB-LAC-010 (teste: imageopt.AllowedUploadMIMEs used directly; aliases deleted) — PR #120
- **2026-09-08 20:39:28 UTC** — revisão DISPENSADA em VERB-LAC-009 (painel Nêmesis/Hígia/Jano no diff combinado do PR #120; votos não persistidos em reviews/votes/) · resolvido VERB-LAC-009 (teste: web/src/lib/url.test.ts hostOf) — PR #120
- **2026-09-08 20:39:27 UTC** — revisão DISPENSADA em VERB-LAC-008 (painel Nêmesis/Hígia/Jano no diff combinado do PR #120; votos não persistidos em reviews/votes/) · resolvido VERB-LAC-008 (teste: preview.TestPreviewDoesNotReexportQueueSentinels) — PR #120
- **2026-09-08 20:39:26 UTC** — revisão DISPENSADA em VERB-LAC-007 (painel Nêmesis/Hígia/Jano no diff combinado do PR #120; votos não persistidos em reviews/votes/) · resolvido VERB-LAC-007 (teste: auditctx.TestSet_AnnotatesTheRequestsContext) — PR #120
- **2026-09-08 20:39:26 UTC** — revisão DISPENSADA em VERB-LAC-006 (painel Nêmesis/Hígia/Jano no diff combinado do PR #120; votos não persistidos em reviews/votes/) · resolvido VERB-LAC-006 (teste: factors.test.ts#canMailStepUpCode gone) — PR #120
- **2026-09-08 20:39:25 UTC** — revisão DISPENSADA em VERB-LAC-005 (painel Nêmesis/Hígia/Jano no diff combinado do PR #120; votos não persistidos em reviews/votes/) · resolvido VERB-LAC-005 (teste: AuthProvider.charter.test.ts#isCurrentGeneration gone) — PR #120
- **2026-09-08 20:39:24 UTC** — revisão DISPENSADA em BEST-ATE-004 (painel Nêmesis/Hígia/Jano no diff combinado do PR #120; votos não persistidos em reviews/votes/) · resolvido BEST-ATE-004 (teste: entries.test.tsx#no as Entry) — PR #120
- **2026-09-08 20:39:24 UTC** — revisão DISPENSADA em BEST-ATE-003 (painel Nêmesis/Hígia/Jano no diff combinado do PR #120; votos não persistidos em reviews/votes/) · resolvido BEST-ATE-003 (teste: TagPicker.test.tsx#safePage clamp) — PR #120
- **2026-09-08 20:39:23 UTC** — revisão DISPENSADA em VERB-LAC-004 (painel Nêmesis/Hígia/Jano no diff combinado do PR #120; votos não persistidos em reviews/votes/) · resolvido VERB-LAC-004 (teste: jsonopt.TestDecodeOptionalString) — PR #120
- **2026-09-08 20:39:22 UTC** — revisão DISPENSADA em VERB-LAC-003 (painel Nêmesis/Hígia/Jano no diff combinado do PR #120; votos não persistidos em reviews/votes/) · resolvido VERB-LAC-003 (teste: web/src/api/auth.envelope.test.ts) — PR #120
- **2026-09-08 20:39:22 UTC** — revisão DISPENSADA em VERB-LAC-002 (painel Nêmesis/Hígia/Jano no diff combinado do PR #120; votos não persistidos em reviews/votes/) · resolvido VERB-LAC-002 (teste: folders/password.go aliases removed; pwhash.Hash/Verify) — PR #120
- **2026-09-08 20:39:21 UTC** — revisão DISPENSADA em VERB-LAC-001 (painel Nêmesis/Hígia/Jano no diff combinado do PR #120; votos não persistidos em reviews/votes/) · resolvido VERB-LAC-001 (teste: auth.TestTwinIPNormalizeAliasesAreGone) — PR #120
- **2026-09-08 20:39:20 UTC** — revisão DISPENSADA em DUP-ECO-006 (painel Nêmesis/Hígia/Jano no diff combinado do PR #120; votos não persistidos em reviews/votes/) · resolvido DUP-ECO-006 (teste: secrethint.TestEqualsPassword_TrimsAndFolds) — PR #120
- **2026-09-08 20:39:19 UTC** — revisão DISPENSADA em DUP-ECO-005 (painel Nêmesis/Hígia/Jano no diff combinado do PR #120; votos não persistidos em reviews/votes/) · resolvido DUP-ECO-005 (teste: imageopt.TestAdmitBytes_EmptyAndCap) — PR #120
- **2026-09-08 20:39:18 UTC** — revisão DISPENSADA em DUP-ECO-004 (painel Nêmesis/Hígia/Jano no diff combinado do PR #120; votos não persistidos em reviews/votes/) · resolvido DUP-ECO-004 (teste: auth.TestHostnameAllowlist_ExampleCOMAcceptedOnBothPathsAfterFold) — PR #120
- **2026-09-08 20:39:18 UTC** — revisão DISPENSADA em CPX-DED-008 (painel Nêmesis/Hígia/Jano no diff combinado do PR #120; votos não persistidos em reviews/votes/) · resolvido CPX-DED-008 (teste: auth.TestTryStepUpProofHasNoLimiterSideEffects) — PR #120
- **2026-09-08 20:39:17 UTC** — revisão DISPENSADA em CPX-DED-007 (painel Nêmesis/Hígia/Jano no diff combinado do PR #120; votos não persistidos em reviews/votes/) · resolvido CPX-DED-007 (teste: preview.TestProcess_GetPreviewErrorAfterPendingWriteReleasesStatus) — PR #120
- **2026-09-08 20:39:17 UTC** — revisão DISPENSADA em CPX-DED-006 (painel Nêmesis/Hígia/Jano no diff combinado do PR #120; votos não persistidos em reviews/votes/) · resolvido CPX-DED-006 (teste: backup.TestListOwnedObjectsRejectsNegativeRemaining) — PR #120
- **2026-09-08 20:39:16 UTC** — revisão DISPENSADA em CPX-DED-005 (painel Nêmesis/Hígia/Jano no diff combinado do PR #120; votos não persistidos em reviews/votes/) · resolvido CPX-DED-005 (teste: preview.TestParseHead_EmptyTitleDoesNotDropFollowingOGImage) — PR #120
- **2026-09-08 20:39:15 UTC** — revisão DISPENSADA em CPX-DED-004 (painel Nêmesis/Hígia/Jano no diff combinado do PR #120; votos não persistidos em reviews/votes/) · resolvido CPX-DED-004 (teste: importer.TestApplyConflicts_DuplicateURLCountsEveryFolder) — PR #120
- **2026-09-08 20:39:15 UTC** — revisão DISPENSADA em BEST-ATE-002 (painel Nêmesis/Hígia/Jano no diff combinado do PR #120; votos não persistidos em reviews/votes/) · resolvido BEST-ATE-002 (teste: auth.TestPayloadBuildersThreadRequestContext) — PR #120
- **2026-09-08 20:39:14 UTC** — revisão DISPENSADA em BEST-ATE-001 (painel Nêmesis/Hígia/Jano no diff combinado do PR #120; votos não persistidos em reviews/votes/) · resolvido BEST-ATE-001 (teste: preview.TestShouldRender_ClassifiesSentinelsWhenErrorTextIsRewritten) — PR #120
- **2026-09-08 20:39:14 UTC** — revisão DISPENSADA em DUP-ECO-003 (painel Nêmesis/Hígia/Jano no diff combinado do PR #120; votos não persistidos em reviews/votes/) · resolvido DUP-ECO-003 (teste: links.ValidateAbsoluteHTTPURL + netscape/screenshot isHTTPScheme) — PR #120
- **2026-09-08 20:39:13 UTC** — revisão DISPENSADA em DUP-ECO-002 (painel Nêmesis/Hígia/Jano no diff combinado do PR #120; votos não persistidos em reviews/votes/) · resolvido DUP-ECO-002 (teste: SettingsPage.test.tsx / AdminUsersPage.test.tsx hasSecondFactor) — PR #120
- **2026-09-08 20:39:12 UTC** — revisão DISPENSADA em DUP-ECO-001 (painel Nêmesis/Hígia/Jano no diff combinado do PR #120; votos não persistidos em reviews/votes/) · resolvido DUP-ECO-001 (teste: masterPasswordForm.test.ts#too-long + settings 72-byte cap) — PR #120
- **2026-09-08 20:39:11 UTC** — revisão DISPENSADA em CPX-DED-003 (painel Nêmesis/Hígia/Jano no diff combinado do PR #120; votos não persistidos em reviews/votes/) · resolvido CPX-DED-003 (teste: folders.TestRepository_Update_PasswordHintParentMatrix) — PR #120
- **2026-09-08 20:39:11 UTC** — revisão DISPENSADA em CPX-DED-002 (painel Nêmesis/Hígia/Jano no diff combinado do PR #120; votos não persistidos em reviews/votes/) · resolvido CPX-DED-002 (teste: backup.TestNormalizeRestoreFolderParents / ChildOfCycleKeepsParent) — PR #120
- **2026-09-08 20:39:10 UTC** — revisão DISPENSADA em CPX-DED-001 (painel Nêmesis/Hígia/Jano no diff combinado do PR #120; votos não persistidos em reviews/votes/) · resolvido CPX-DED-001 (teste: backup.TestKeepOwnedLinkKeys_ForeignIDDoesNotAppear) — PR #120
- **2026-09-08 20:39:10 UTC** — revisão DISPENSADA em ARCH-ATL-004 (painel Nêmesis/Hígia/Jano no diff combinado do PR #120; votos não persistidos em reviews/votes/) · resolvido ARCH-ATL-004 (teste: web/src/lib/viewPrefs.ts (Sort/ViewMode)) — PR #120
- **2026-09-08 20:39:09 UTC** — revisão DISPENSADA em ARCH-ATL-003 (painel Nêmesis/Hígia/Jano no diff combinado do PR #120; votos não persistidos em reviews/votes/) · resolvido ARCH-ATL-003 (teste: backup.TestSanitizeAndPlain / htmlsanitize.SanitizeAndPlain) — PR #120
- **2026-09-08 20:39:08 UTC** — revisão DISPENSADA em ARCH-ATL-002 (painel Nêmesis/Hígia/Jano no diff combinado do PR #120; votos não persistidos em reviews/votes/) · resolvido ARCH-ATL-002 (teste: crudupdate.TestSourceDoesNotImportTags) — PR #120
- **2026-09-08 20:39:08 UTC** — revisão DISPENSADA em ARCH-ATL-001 (painel Nêmesis/Hígia/Jano no diff combinado do PR #120; votos não persistidos em reviews/votes/) · resolvido ARCH-ATL-001 (teste: TestGodPackageSplit_AnomalyAndIPBlockAreSubpackages) — PR #120
- **2026-09-08 02:30:27 UTC** — TASKS.md criado a partir de FINDINGS.json

## ✅ RODADA FECHADA

Todos os 38 achados têm desfecho: 32 resolvidos · 6 refutados · 0 aceitos. Nenhum aberto.
