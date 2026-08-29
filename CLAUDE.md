# CLAUDE.md — Foldex project invariants

> Defaults for every change in this repo. Override only with a one-line note in the PR description. **WHAT** must hold lives here; **WHY** in long form lives in `docs/ARCHITECTURE.md` ADRs.

## 0. What is Foldex

A **self-hosted, multi-user bookmark manager** — save links from the browser (UI, ⌘K palette, MV3 extension), tag them (M:N), organize in nestable folders (1:N), track clicks via `/go/{slug}`, pin favorites, monitor opt-in pages for changes via Web Push, import/export to Netscape HTML + versioned JSON, and full DB+RustFS backup ZIPs.

**Multi-user is COMPLETE and ON by default (ADR-30/31/32/33/34/35 / `docs/SDD-AUTH-RBAC.md`).** `AUTH_ENABLED` defaults to `1` since PR4: sessions, invitations, four-role RBAC with a permission matrix, TOTP + recovery codes + e-mail OTP, password reset, Google sign-in with password-proved conversion, API tokens for the extension, an administration surface (users, roles, invitations, audit trail, instance policy) and owner-configurable password/OAuth rules. Every content row has an owner and every repository method is scoped — §4's ownership invariants are the ones a new query must honour.

Threat model: **an authenticated multi-tenant app.** Content is private per account, administrators included; an admin manages accounts and never reads another account's rows. Backend still listens on `127.0.0.1` by default and expects nginx in front for anything else. `AUTH_ENABLED=0` remains a supported escape hatch for a single-user machine on a private network — on that setting every request is attributed to the bootstrap administrator (resolved as `owner`, since a role that cannot change policy could not fix the very lockout the escape hatch exists for), and anyone who can reach the port owns the library.

`SHARED_SECRET` is **removed**: a perimeter header that authenticated nobody and scoped nothing, it was deprecated by ADR-30 and deleted outright — the env var, the `X-Foldex-Secret` header, the router guard and the extension/SPA plumbing are all gone. Any `/api/*` protection is the auth stack's job.

## 1. Always run on the latest stable versions

When upgrading or scaffolding, check the actual latest stable before pinning — `go.dev/dl`, `nodejs.org`, `hub.docker.com/r/oven/bun/tags`, and `npm view <pkg> version --registry=https://registry.npmjs.org/` (always public registry, never a local mirror).

A tabela de pins (com o porquê de cada um e as pegadinhas de upgrade) vive em [`docs/STACK.md`](docs/STACK.md) — **consulte-a antes de mexer em `backend/go.mod` ou `web/package.json`**, e atualize-a no mesmo change de qualquer bump. Verifique com `go list -m all` / `bun pm ls`. Bump direto se minor/patch; avalie breaking changes se major. Package manager: **bun ≥ 1.3**.

## 2. Always write tests — coverage gate is 85%

For every new function/handler/component/hook: write the test in the same change. Backend = `testify` + `testcontainers-go` (build-tag `integration`). Frontend = `Vitest` + `@testing-library/react` with axios mocked via `src/test/server.ts` (keep in sync with backend changes).

**Coverage thresholds (enforced in CI/Makefile):** ≥85% statements, ≥80% branches (frontend). Backend test execution covers `./...`; measurement excludes boot/helpers (`cmd/server`, `cmd/rustfs-bootstrap`, `internal/db`, `internal/testdb`, `internal/pkg/authctxtest`). Frontend measurement excludes `src/main.tsx`, `src/theme/**`, `src/test/**`. **`src/api/client.ts` is NO LONGER excluded** — it stopped being a thin axios wrapper when ADR-30 put the CSRF header injection and the single-flight refresh in it, and both are security contracts that need to be measured. `vitest.config.ts` also sets `testTimeout: 20s`: the 5s default is fine for the plain run but not under v8 coverage instrumentation, where the heavier dialog tests trip it and fail as timeouts that say nothing about the code.

```bash
make test-backend / test-integration / coverage-backend
cd web && bun run test / coverage
```

Two Makefile gotchas (both burned in): `-covermode=atomic` (default deflates under `-coverpkg`) and `-count=1` (without it, cached test profiles silently show old coverage).

**A package that calls `testdb.Shared` MUST stop it from `TestMain`, and `internal/security` enforces that.** `Shared` starts one Postgres per test BINARY and holds it in a package-level `sync.Once` — deliberately, because the alternative was 171 container starts per run whose failures ("connection refused", "unexpected EOF" during migrations) were never bugs in the code under test. The cost is that nothing inside a test can terminate it: a `t.Cleanup` on whichever test ran first would kill the container while the rest of the package still needed it, so only `TestMain` after `m.Run()` is late enough. **There is no safety net behind this** — the Makefile exports `TESTCONTAINERS_RYUK_DISABLED=true`, so testcontainers' reaper is not running, and a package that forgets the hook leaks a container until the machine is rebooted. That is not hypothetical: one package missing it produced 25 orphans, some seventeen hours old, a daemon taking 28 s to answer `docker info`, and integration failures that read as hung tests while pointing nowhere near the cause. `TestEveryPackageUsingTestdbStopsIt` and `TestStopSharedIsOnlyCalledFromTestMain` match on the AST call, never on file text — an earlier text-matching draft flagged itself, because its own failure message names the function, and a guard that fails for the wrong reason teaches people to route around it. `testdb.New` is unaffected: it registers its own `t.Cleanup`. A package with a second shared container of its own kind (only `mailoutbox`, for RabbitMQ) terminates it in the same `TestMain`. **`TEST_PARALLEL` (default 4) caps package concurrency** because Go defaults `-p` to `GOMAXPROCS` and each database package starts its own Postgres — asking a laptop daemon for ~19 at once stretches startup past Go's 10-minute package timeout, and the resulting report says "hung test" rather than "Docker was saturated".

## 3. Always update documentation when behavior changes

| Change to … | Update … |
|---|---|
| Feature scope, goals, MVP boundary | `docs/VISION.md` |
| API surface, data model, stack, ADRs | `docs/ARCHITECTURE.md` |
| Task done / lessons learned / followups | `docs/TASKS.md` (append to "Log de conclusão") |
| Stack version bump | `docs/ARCHITECTURE.md` + this `CLAUDE.md` §1 table + `README.md` stack line |
| **Any user-visible feature, flow, quickstart, smoke test, shortcut, stack version, or screenshot** | **`README.md` (source) AND `README.pt-BR.md` (mirror — keep in parity)** |
| Browser extension behavior | `extension/README.md` |
| Database schema (migration) | `docs/ARCHITECTURE.md` (data model) + comment block at top of `.up.sql` |

**The README is NOT optional.** Treat `README.md` as part of the product surface: if a change alters what a user sees or does, or what the stack is, the README MUST be updated in the same change — and the `README.pt-BR.md` mirror kept in sync. Before declaring done, re-read the README sections your change touches and confirm none went stale (versions, feature table, shortcuts, smoke test, screenshots). A change that ships code but skips doc updates — README included — is **incomplete**.

## 4. Data invariants — what must always hold

Uma linha = uma regra. O **porquê**, a consequência observada e o detalhe estão em [`docs/INVARIANTS.md`](docs/INVARIANTS.md), um heading por item (`INV-NNN`) — leia o item antes de mexer na área dele.

**Regras com `guard:` falham o build ou a CI. Quando uma delas quebrar, leia a mensagem do teste: ela contém o raciocínio inteiro.**

### 4.1 Segurança e multi-tenancy — nunca viole, leia sempre

- **Every content row has an owner, and identity travels as an explicit parameter** → [INV-001](docs/INVARIANTS.md#inv-001) | guard: `TestNoUnscopedTenantQueries`
- **The TOTP seed is ENCRYPTED, never hashed — and its key is not regenerable** → [INV-002](docs/INVARIANTS.md#inv-002)
- **A TOTP code is spent when it is used — the replay guard is a CONDITIONAL UPDATE, not a Go comparison** → [INV-003](docs/INVARIANTS.md#inv-003)
- **A six-digit code and a recovery code are told apart by SEPARATOR-STRIPPED LENGTH, never by digit count** → [INV-004](docs/INVARIANTS.md#inv-004) | guard: `TestRecoveryCode_WithSixDigitsIsNotMistakenForATOTPCode`
- **One channel must never satisfy both factors** → [INV-005](docs/INVARIANTS.md#inv-005) | guard: `TestReset_MailboxAloneCannotSatisfyBothFactors`, `TestEmailOTP_IsNotOfferedWhenMailOnlyGoesToTheLog`
- **Credentials are redacted at the ROOT log handler, not at each call site** → [INV-006](docs/INVARIANTS.md#inv-006) | guard: `TestRedactionListMatchesTheDocumentedSet`
- **`X-Forwarded-For` is honoured ONLY from a configured proxy** → [INV-007](docs/INVARIANTS.md#inv-007)
- **Nenhuma entrada controlada pelo cliente compõe uma chave de rate limit** → [INV-183](docs/INVARIANTS.md#inv-183)
  ↳ `IP + User-Agent` daria ao atacante um orçamento novo por requisição; o balde deixa de existir enquanto continua parecendo existir. Registrar ≠ confiar.
- **O balde de IP do login conta LARGURA (contas distintas); o de e-mail conta PROFUNDIDADE; conjunto cheio TRANCA** → [INV-184](docs/INVARIANTS.md#inv-184) | guard: `TestLoginFailure_TheIPBucketCountsAccounts_NotAttempts`, `TestLogin_ManyPeopleBehindOneAddressDoNotLockEachOtherOut`, `TestSetMode_ASuccessDoesNotForgiveTheAccountsAlreadySwept`, `TestSetMode_MembersAgeOutOfTheWindow`, `TestEveryTerminalPathReleasesTheReservation`, `TestLogin_AliasesOfOneAccountCostTheSameAsStrangers`
  ↳ `gcLocked` media só `e.fails`: um `Release` apagava o conjunto inteiro, e martelar uma conta devolvia todo o orçamento de largura da origem. Achado por mutação.
- **Limites de abuso: pisos dos DOIS lados, fora de faixa reverte o CAMPO, e "dinâmico" é RECARREGAR** → [INV-185](docs/INVARIANTS.md#inv-185) | guard: `TestValidateForWrite_RefusesBothDirections`, `TestSanitize_RevertsOneKnobAndKeepsTheRest`, `TestCache_FailStaticKeepsTheLastGoodPolicy`
  ↳ Um rate limit baixo demais VIRA o ataque: 1 conta/hora tranca um escritório com uma senha errada.
- **O detector de anomalias RELATA; o bloqueio é do humano, e o painel não mostra e-mails** → [INV-186](docs/INVARIANTS.md#inv-186) | guard: `TestAnomaly_CarriesNoEmailAnywhereInItsJSON`
- **E-mail confirmation is a LINK, not a code, and its endpoint is unauthenticated** → [INV-008](docs/INVARIANTS.md#inv-008)
- **`Optional` enforces CSRF on unsafe verbs** → [INV-009](docs/INVARIANTS.md#inv-009)
- **Second-factor budgets live in the DATABASE, not in `attemptlimit`** → [INV-010](docs/INVARIANTS.md#inv-010)
- **Every pre-auth challenge, password-reset link and session-authenticated credential proof is bound to the live credential epoch** → [INV-011](docs/INVARIANTS.md#inv-011)
- **A username is OPTIONAL, and its ban on `@` is what keeps two namespaces apart** → [INV-012](docs/INVARIANTS.md#inv-012)
- **There is a username availability probe and there is deliberately NO e-mail one outside administration** → [INV-013](docs/INVARIANTS.md#inv-013) | guard: `TestAvailability_EmailProbeIsMountedOnlyUnderAdmin`
- **The account's e-mail moves only after the NEW address confirms, and the OLD one is told without a link** → [INV-014](docs/INVARIANTS.md#inv-014)
- **A Google `sub` is the ONLY key that resolves an OAuth login; a matching e-mail opens CONVERSION, never a session** → [INV-015](docs/INVARIANTS.md#inv-015) | guard: `TestOAuth_EmailMatchAloneNeverIssuesASession`
- **OAuth never skips the second factor** → [INV-016](docs/INVARIANTS.md#inv-016) | guard: `TestOAuth_ConvertStillRequiresTheSecondFactor`
- **The Google subject travels on the CHALLENGE ROW, never through the browser** → [INV-017](docs/INVARIANTS.md#inv-017)
- **Linking an OAuth identity requires a fresh credential proof, not only a session** → [INV-018](docs/INVARIANTS.md#inv-018)
- **`userColumns` is fully qualified with `app_user.`, and that is load-bearing** → [INV-019](docs/INVARIANTS.md#inv-019)
- **An ACTIVE account always holds at least one credential, and the DATABASE is what guarantees it** → [INV-020](docs/INVARIANTS.md#inv-020)
- **`POST /api/admin/users` is a DECLARED EXCEPTION to the rule below, taken by the instance owner** → [INV-021](docs/INVARIANTS.md#inv-021) | guard: `TestAdminCreateUser_*`
- **An administrator never chooses, installs or receives another user's credential** → [INV-022](docs/INVARIANTS.md#inv-022)
- **API tokens are scoped to CONTENT, and the scope is enforced by middleware, not by discipline** → [INV-023](docs/INVARIANTS.md#inv-023)
- **A cota da API autenticada é por PRINCIPAL, conta só escrita, e não isenta ninguém** → [INV-181](docs/INVARIANTS.md#inv-181) | guard: `TestAPIQuota_TwoPrincipalsHaveIndependentBudgets`, `TestAPIQuota_TheOwnerIsNotExempt`, `TestExpensiveRoutes_EveryPatternNamesARouteTheRouterMounts`, `TestWiring_APIQuotaRefusesAWriteLoopThroughTheRealRouter`
  ↳ Por rota, um laço espalhado por vinte endpoints fica dentro do limite em cada um e segura o pool inteiro; `attemptlimit` não serve porque conta falhas consecutivas e `CommitSuccess` zera o contador.
- **O clique público é coalescido em MEMÓRIA, por visitante hasheado, e a supressão nunca alcança o redirect** → [INV-182](docs/INVARIANTS.md#inv-182) | guard: `TestClickCoalesce_ARepeatVisitWritesOneRowAndStillRedirects`, `TestClickCoalesce_TheMapNeverExceedsItsCeiling`, `TestWiring_RepeatClicksFromOneVisitorWriteOneRowAndStillRedirect`
  ↳ Sem teto, o coalescedor é o novo alvo: o atacante enche o mapa em vez do `click_log`.
- **`/go/{42}` and `/n/{42}` are OFF by default** → [INV-024](docs/INVARIANTS.md#inv-024)
- **`totp_enabled` and `email_2fa_enabled` are derived with `EXISTS`, never stored, and `HasSecondFactor()` is their OR** → [INV-025](docs/INVARIANTS.md#inv-025)
- **"May this factor be removed?" is answered in ONE place** → [INV-026](docs/INVARIANTS.md#inv-026)
- **A session step-up accepts TOTP, a recovery code or a mailed code, and the proof is VERIFIED without being spent** → [INV-027](docs/INVARIANTS.md#inv-027)
- **Recovery codes and six-digit e-mail OTPs use keyed, context-bound digests and remain single-use by conditional UPDATE** → [INV-028](docs/INVARIANTS.md#inv-028)
- **`POST /api/auth/password/forgot` ALWAYS answers 202, on three channels** → [INV-029](docs/INVARIANTS.md#inv-029)
- **Auth mail is written to a TRANSACTIONAL OUTBOX, in the same transaction as the credential it carries** → [INV-030](docs/INVARIANTS.md#inv-030)
- **The mail TRANSPORT is pluggable, and only the sink changes** → [INV-031](docs/INVARIANTS.md#inv-031)
- **Every auth e-mail renders from an embedded template at DELIVERY time, in the recipient's locale** → [INV-035](docs/INVARIANTS.md#inv-035) | guard: `TestLinklessMessagesCannotBeGivenALink`, `TestEveryLinkCarryingMessageRendersItsLinkInBothArms`
- **E-mail credentials live in URL fragments, never queries or access-log-visible paths** → [INV-036](docs/INVARIANTS.md#inv-036)
- **A password reset proves the FIRST factor only** → [INV-037](docs/INVARIANTS.md#inv-037) | guard: `TestResetPassword_StillRequiresTheSecondFactor`
- **`AUTH_REQUIRE_2FA_FOR_ADMINS` diverts, it does not refuse, and has no privileged-session exception** → [INV-038](docs/INVARIANTS.md#inv-038)
- **Sessions are opaque tokens stored as sha256, and the CSRF header is checked against the SESSION ROW** → [INV-039](docs/INVARIANTS.md#inv-039)
- **Refresh rotation runs in ONE `SERIALIZABLE` transaction, and a replayed token kills the whole FAMILY** → [INV-040](docs/INVARIANTS.md#inv-040) | guard: `TestRefresh_GraceSiblingInheritsFamilyAndAbsoluteCeiling`
- **Login is byte-identical for unknown e-mail, wrong password and disabled account** → [INV-041](docs/INVARIANTS.md#inv-041)
- **`/api/auth/me` ALWAYS answers 200** → [INV-042](docs/INVARIANTS.md#inv-042)
- **A non-admin gets 404 from `/api/admin/*`, not 403** → [INV-043](docs/INVARIANTS.md#inv-043)
- **No API call may leave the instance with zero active administrators** → [INV-044](docs/INVARIANTS.md#inv-044)
- **RBAC is four roles and a permission matrix, and content stays private per account** → [INV-045](docs/INVARIANTS.md#inv-045)
- **The RBAC matrix is CONFIGURABLE, and a compiled floor is what the configuration cannot reach** → [INV-167](docs/INVARIANTS.md#inv-167) | guard: `TestLoad_AnEmptyTableIsNotAnUnrecoverableInstance`, `TestSet_AdminCannotGrantOwnerLevelPowers`, `TestSchema_RefusesAnOwnerRow`
- **There is EXACTLY ONE owner, enforced by a partial unique index, and the seat moves only by transfer** → [INV-046](docs/INVARIANTS.md#inv-046)
- **The audit trail outlives the accounts it describes** → [INV-047](docs/INVARIANTS.md#inv-047)
- **Instance policy has FLOORS that configuration cannot cross, and writing it is owner-only** → [INV-048](docs/INVARIANTS.md#inv-048)
- **The password floor's write bound is bcrypt's 72 bytes, and its READ bound is not** → [INV-169](docs/INVARIANTS.md#inv-169) | guard: `TestPasswordFloor_WriteBoundTightensAndReadBoundDoesNot`
- **`google_auto_provision` revokes ADR-31's invite-only rule, and every safeguard is load-bearing** → [INV-049](docs/INVARIANTS.md#inv-049)
- **A row belonging to another user is reported 404, never 403** → [INV-050](docs/INVARIANTS.md#inv-050) | guard: `TestCrossUser_GetOfAnotherUsersRowIsNotFound`
- **Cross-tenant references are blocked by the DATABASE, not by handler discipline** → [INV-051](docs/INVARIANTS.md#inv-051)
- **`note.body_html` is sanitized server-side on every write, no exceptions** → [INV-059](docs/INVARIANTS.md#inv-059)
- **Cloud metadata ranges and RFC6598 shared address space (`100.64.0.0/10`) are always blocked** → [INV-079](docs/INVARIANTS.md#inv-079)
- **SSRF dialer is checked twice** → [INV-080](docs/INVARIANTS.md#inv-080)
- **Screenshot is a FALLBACK, never default** → [INV-083](docs/INVARIANTS.md#inv-083)
- **Every screenshot capture gets a fresh Chromium BrowserContext and a per-capture strict local proxy** → [INV-084](docs/INVARIANTS.md#inv-084)
- **Manual screenshot endpoint applies the same SSRF gate** → [INV-085](docs/INVARIANTS.md#inv-085)
- **Screenshot resource budgets are fail-closed** → [INV-086](docs/INVARIANTS.md#inv-086)
- **`GET /api/links/url-metadata` reuses the preview `Fetcher` — same SSRF posture, no duplicate HTTP client** → [INV-087](docs/INVARIANTS.md#inv-087)
- **oEmbed enrichment reuses the SAME `preview.Fetcher` client — never a second HTTP stack** → [INV-088](docs/INVARIANTS.md#inv-088)
- **The cookie `Secure` flag is derived from `AUTH_PUBLIC_URL`'s scheme, NOT from the bind address** → [INV-091](docs/INVARIANTS.md#inv-091) | guard: `TestLoad_CookieSecureFollowsThePublicURLScheme`
- **`SetSession` expires `fx_pa`** → [INV-092](docs/INVARIANTS.md#inv-092)
- **Boot refuses the insecure-by-default combo** → [INV-093](docs/INVARIANTS.md#inv-093)
- **Backup carries NO auth material, and restore always writes rows owned by the CALLER** → [INV-105](docs/INVARIANTS.md#inv-105) | guard: `TestCrossUser_RestoreIgnoresOwnerEmail`
  ↳ Wipe com `DeleteObjectsPrefix` destruiria os arquivos de TODOS os tenants; por isso a lista explícita de chaves.
- **Public note-media references are read capabilities only, never write/delete authority** → [INV-106](docs/INVARIANTS.md#inv-106)
- **The `foldex-web` image NEVER ships a private TLS key** → [INV-109](docs/INVARIANTS.md#inv-109)
- **VAPID private key is `0o600` and never baked into the image** → [INV-117](docs/INVARIANTS.md#inv-117)
- **`audit_log.subject` é CONTEÚDO, e existe exatamente UMA consulta que o lê** → [INV-175](docs/INVARIANTS.md#inv-175) | guard: `TestAuditSubjectIsSelectedByExactlyOneQuery`, `TestAudit_AdminNeverSeesAContentSubjectOrItsActorEmail`
  ↳ O guard nasceu andando só por `FuncDecl` e passou com a coluna adicionada direto na const `adminProjection` — a edição mais provável era a que ele não via.
  ↳ A busca também precisou de trava: `?category=content&q=alice@…` casava em `actor_email` e devolvia o `actor_ref` dela. Coluna escondida na SAÍDA e selecionável na ENTRADA não é enforcement.
- **`ip`, `ip_trusted` e `user_agent` são um CONJUNTO** → [INV-176](docs/INVARIANTS.md#inv-176) | guard: `TestAuditProvenance_*`
- **A cobertura de conteúdo é um MIDDLEWARE; o rótulo é opcional por construção** → [INV-177](docs/INVARIANTS.md#inv-177) | guard: `TestWiring_ContentAuditRecordsAMutationThroughTheRealRouter`
- **O bloqueio permanente de IP é owner-only e TRAVADO, e o cache falha ABERTO** → [INV-178](docs/INVARIANTS.md#inv-178) | guard: `TestValidateBlockIP_*`, `TestBlocklist_FailsOpenWhenTheLoadErrors`, `TestWiring_BlocklistGateRefusesBeforeRouting`
- **A request span identifies its caller by OPAQUE ID ONLY, and the annotation hangs off principal CREATION, not a route group** → [INV-170](docs/INVARIANTS.md#inv-170) | guard: `TestEveryPrincipalSeamAnnotatesTheSpan`, `TestAnnotatePrincipalRecordsNoIdentifyingAttributeBeyondTheOpaqueID`, `TestAuthenticate_StampsUserIDOnSpansOfTheAuthSurfaceItself`, `TestQuerySpansNeverCarryThePostgresRole`
  ↳ Montado num grupo de rotas, perdeu em silêncio toda a metade autenticada de `/api/auth`.
  ↳ `user.name` do `otelpgx` é o PAPEL do Postgres: ao lado de `user.id` vira "requests por usuário" respondendo `user_foldex` para 100% do tráfego.

- **As credenciais do S3 externo existem SÓ no processo do backup-agent, e o dump operacional é cifrado por DEFAULT** → [INV-171](docs/INVARIANTS.md#inv-171) | guard: `TestLoad_PlaintextIsAnExplicitOptOutNeverAFallback`, `TestDumpRun_ShipsAnEncryptedVerifiableArtifact`, `TestAgent_HeartbeatCarriesNoCredential`
  ↳ O heartbeat publica o ENDEREÇO do bucket (`{endpoint, bucket, prefix}`) para a tela poder mirar; o acesso a ele, nunca.
- **Um job de backup roda no máximo uma vez por vez, e a exclusão tem TRÊS camadas que se cobrem** → [INV-172](docs/INVARIANTS.md#inv-172) | guard: `TestBegin_TheRunningSlotIsExclusivePerJob`, `TestExpireStale_FreesTheSlotADeadAgentHeld`
- **A env decide QUAIS jobs de backup existem; o banco decide QUANDO rodam; pisos compilados seguram o mínimo** → [INV-173](docs/INVARIANTS.md#inv-173) | guard: `TestValidateJobConfig_FloorsHold`, `TestSchedule_WriteIsOwnerOnlyThroughTheLockedPermission`, `TestAgent_HeartbeatCarriesTheEnvBaseline`
  ↳ Um vocabulário por job (ADR-44) não deixava nenhum job escolher dias da semana; o ADR-45 unificou a forma e manteve os pisos, com o do `dump` maior que o dos outros.
### 4.2 Contratos de dados — consulte ao tocar em schema, query ou payload

- **`tag.name` is unique PER USER** → [INV-052](docs/INVARIANTS.md#inv-052)
- **`tag.color` / `folder.color` are CSS strings** → [INV-053](docs/INVARIANTS.md#inv-053)
- **`link.url` is unique PER USER** → [INV-054](docs/INVARIANTS.md#inv-054)
- **`link.slug` is NOT NULL and GLOBALLY UNIQUE** → [INV-055](docs/INVARIANTS.md#inv-055)
- **`click_log` is the single source of truth for clicks** → [INV-056](docs/INVARIANTS.md#inv-056)
- **`link_tag` is the only place link↔tag lives** → [INV-057](docs/INVARIANTS.md#inv-057)
- **`link_tag` and `click_log` are polymorphic (mig 000014) — shared by `link` and `note` via an `entity_kind ∈ {'link','note'}` discriminator on `entity_id`** → [INV-058](docs/INVARIANTS.md#inv-058) | guard: `TestCrossContamination_LinkAndNoteRowsDoNotLeak`
- **Ordenação crescente na trilha é uma CONSULTA diferente, nunca a página invertida** → [INV-179](docs/INVARIANTS.md#inv-179)
- **O balde do dia é construído no BANCO; a data local do processo não é a data das linhas** → [INV-180](docs/INVARIANTS.md#inv-180)
  ↳ Com o servidor em UTC-3 o último balde terminava onde o dia começava: a coluna mais movimentada lia zero, e parecia "um dia quieto".
- **`internal/entries` (`GET /api/entries`) is the single, read-only source for the interleaved link+note grid** → [INV-060](docs/INVARIANTS.md#inv-060)
- **`preview_status ∈ {pending, ok, failed}`** → [INV-061](docs/INVARIANTS.md#inv-061)
- **Change-check claims carry their configuration snapshot** → [INV-062](docs/INVARIANTS.md#inv-062)
- **Folders are 1:N exclusive AND nestable** → [INV-063](docs/INVARIANTS.md#inv-063)
- **A folder cascade never crosses an unproved password boundary** → [INV-064](docs/INVARIANTS.md#inv-064)
- **Folder password protection is per-folder, backend-enforced, and split into two separate mechanisms — never conflate them** → [INV-065](docs/INVARIANTS.md#inv-065)
- **Master password is RECOVERY ONLY, never a view bypass** → [INV-066](docs/INVARIANTS.md#inv-066)
- **`folder.password_hint` is NON-secret and MUST NEVER equal the password** → [INV-067](docs/INVARIANTS.md#inv-067)
- **Home view excludes links inside folders** → [INV-068](docs/INVARIANTS.md#inv-068)
- **Tag filter and folder scope compose via AND** → [INV-069](docs/INVARIANTS.md#inv-069)
- **Internal IDs never appear in the URL** → [INV-070](docs/INVARIANTS.md#inv-070)
- **Folders come BEFORE links in the grid except in alpha sort** → [INV-071](docs/INVARIANTS.md#inv-071)
- **viewMode + foldersCompact are per-context** → [INV-072](docs/INVARIANTS.md#inv-072)
- **FolderCard `compact` mode + RapidView popover** → [INV-073](docs/INVARIANTS.md#inv-073)
- **Drag-and-drop wiring** → [INV-074](docs/INVARIANTS.md#inv-074)
- **Imports are idempotent by URL** → [INV-075](docs/INVARIANTS.md#inv-075)
- **Image input has a 50 MP decode cap** → [INV-076](docs/INVARIANTS.md#inv-076)
- **Uploads and screenshots are always re-encoded via `internal/imageopt`** → [INV-077](docs/INVARIANTS.md#inv-077)
- **Old image objects are purged only after database publication succeeds** → [INV-078](docs/INVARIANTS.md#inv-078)
- **A stored image that turns out to be GONE re-arms its own preview, and the sentinel that gates it is the whole safety of the feature** → [INV-081](docs/INVARIANTS.md#inv-081)
  ↳ Sentinela larga = uma indisponibilidade momentânea limpa todo `og_image_url` e recaptura a biblioteca inteira.
- **A card whose image fails to load falls back to its glyph, never to the browser's broken-image icon** → [INV-082](docs/INVARIANTS.md#inv-082)
- **JSON request bodies are capped at 64 KiB** → [INV-089](docs/INVARIANTS.md#inv-089)
- **Stats handler clamps every numeric knob via `clampInt`** → [INV-090](docs/INVARIANTS.md#inv-090)
- **Backup is a complete DB + RustFS snapshot ZIP** → [INV-102](docs/INVARIANTS.md#inv-102)
- **Every backup operation is admitted before work; export and restore stream** → [INV-103](docs/INVARIANTS.md#inv-103)
- **Backup restore is idempotent by default, never atomic across DB+RustFS** → [INV-104](docs/INVARIANTS.md#inv-104)
- **Backup endpoints require RustFS** → [INV-107](docs/INVARIANTS.md#inv-107)
- **`preview.Worker.Enqueue` returns an error** → [INV-108](docs/INVARIANTS.md#inv-108)
- **The change-check worker reuses the preview `Fetcher` — never duplicate SSRF guards** → [INV-110](docs/INVARIANTS.md#inv-110)
- **`link.last_fingerprint` is prefixed `feed:<hex>` or `content:<hex>`** → [INV-111](docs/INVARIANTS.md#inv-111)
- **First observation never counts as a change** → [INV-112](docs/INVARIANTS.md#inv-112)
- **Opt-out clears the full change-check column group** → [INV-113](docs/INVARIANTS.md#inv-113)
- **Manual `/api/links/{id}/seen-change` is a no-op when `last_change_detected_at IS NULL`** → [INV-114](docs/INVARIANTS.md#inv-114)
- **`push_subscription.endpoint` is GLOBALLY UNIQUE; upsert is the only INSERT path** → [INV-115](docs/INVARIANTS.md#inv-115)
- **404/410 from the push service removes the subscription row** → [INV-116](docs/INVARIANTS.md#inv-116)
- **Web Push delivery is decoupled but lifecycle-owned** → [INV-118](docs/INVARIANTS.md#inv-118)
- **Service Worker is hand-rolled — no `workbox-*` runtime deps** → [INV-119](docs/INVARIANTS.md#inv-119)

### 4.3 Operação, CI e infraestrutura

- **`${VAR:?...}` in a compose file is a WHOLE-FILE gate, never a per-service one** → [INV-032](docs/INVARIANTS.md#inv-032)
  ↳ Já derrubou a stack padrão duas vezes — um `:?` num serviço que o operador nem executa recusa os que ele executa.
- **A Makefile recipe that names a host path uses `$(CURDIR)`, never `$(PWD)`, and a bind mount whose source is missing is not an error** → [INV-033](docs/INVARIANTS.md#inv-033) | guard: `scripts/test-make-migrate-path.sh`
  ↳ Falhou em silêncio e se autocancelou: o backend mandava rodar `make migrate-up`, que não fazia nada e saía 0.
- **A publisher with no consumer is a WARNING, and a send is a LOG LINE** → [INV-034](docs/INVARIANTS.md#inv-034)
  ↳ Aconteceu numa instância viva: publish confirmado, linha `published`, fila enchendo, worker sem log de sucesso.
- **nginx ships defense-in-depth headers** → [INV-094](docs/INVARIANTS.md#inv-094) | guard: `scripts/test-nginx-headers.sh`
- **All CI actions are SHA-pinned, not tag-pinned** → [INV-095](docs/INVARIANTS.md#inv-095)
- **CI uses the GitHub runner's host Docker daemon, not a `docker:dind` service** → [INV-096](docs/INVARIANTS.md#inv-096)
- **A tag push can never publish** → [INV-097](docs/INVARIANTS.md#inv-097)
- **Vite dev is loopback-only by default** → [INV-098](docs/INVARIANTS.md#inv-098)
- **RustFS has no shipped secret** → [INV-099](docs/INVARIANTS.md#inv-099)
- **`.env` is never committed** → [INV-100](docs/INVARIANTS.md#inv-100)
- **Postgres credentials live in `POSTGRES_*` only — `DB_URL` is derived** → [INV-101](docs/INVARIANTS.md#inv-101)

## 5. UI/UX invariants — interaction contracts

Part of the product contract, not nice-to-haves. Uma linha = uma regra; o porquê e a história em [`docs/INVARIANTS.md`](docs/INVARIANTS.md) (`INV-120…165`) — leia o item antes de mexer na área. Regras com `guard:` falham a CI; a mensagem do teste carrega o raciocínio.

- **A mount-time request uses a ref guard and NO per-effect `alive` flag** → [INV-120](docs/INVARIANTS.md#inv-120) | guard: `strictMode.test.tsx`
  ↳ Duas telas shiparam presas num spinner eterno, sobre um token de uso único já gasto.
- **Every dialog closes on `Esc`** → [INV-121](docs/INVARIANTS.md#inv-121)
- **Destructive actions** → [INV-122](docs/INVARIANTS.md#inv-122)
- **A NEW folder or tag opens on a SUGGESTED colour, and only a new one** → [INV-123](docs/INVARIANTS.md#inv-123)
- **Inline tag creation in LinkDialog/NoteDialog is deferred and atomic with parent save** → [INV-124](docs/INVARIANTS.md#inv-124)
- **LinkDialog auto-fills Title/Description from the URL after a 500 ms debounce** → [INV-125](docs/INVARIANTS.md#inv-125)
- **Tooltips are CSS-only via `data-tooltip` (+ optional `data-tooltip-side`)** → [INV-126](docs/INVARIANTS.md#inv-126) | guard: `TooltipPortal.test.tsx`
- **Sidebar stays clean** → [INV-127](docs/INVARIANTS.md#inv-127)
- **Pinned links always come first** → [INV-128](docs/INVARIANTS.md#inv-128)
- **Notes are interleaved with links in the same grid, not a separate section** → [INV-129](docs/INVARIANTS.md#inv-129)
- **Reading a note happens IN the app; `/n/{slug}` is the SHARE link, not the reader** → [INV-130](docs/INVARIANTS.md#inv-130)
- **Note `body_html` is user-authored rich HTML rendered raw (`template.HTML` / no client-side escaping) — the sanitization invariant in §4 is what makes that safe, not an assumption** → [INV-131](docs/INVARIANTS.md#inv-131)
- **Grid is row-major and density is user-controlled** → [INV-132](docs/INVARIANTS.md#inv-132)
- **Card preview area has a fixed height** → [INV-133](docs/INVARIANTS.md#inv-133)
- **"preview falhou" hides when an image is already present** → [INV-134](docs/INVARIANTS.md#inv-134)
- **`localStorage` is the persistence layer for UI prefs** → [INV-135](docs/INVARIANTS.md#inv-135)
- **`/go/{id-or-slug}` button label says "Acessar"** → [INV-136](docs/INVARIANTS.md#inv-136)
- **The OTP field is six inputs, and every one of its keyboard behaviours is a contract** → [INV-137](docs/INVARIANTS.md#inv-137) | guard: `OtpInput.test.tsx`
- **The second-factor UI renders per METHOD, and never re-derives the server's policy** → [INV-138](docs/INVARIANTS.md#inv-138)
- **Recovery codes are shown exactly once, behind an explicit acknowledgement** → [INV-139](docs/INVARIANTS.md#inv-139)
- **All keyboard shortcuts are Alt-based** → [INV-140](docs/INVARIANTS.md#inv-140)
- **Pasting a URL anywhere opens the New Link dialog with it pre-filled** → [INV-141](docs/INVARIANTS.md#inv-141)
- **Dark mode is neutral charcoal/slate, not purple** → [INV-142](docs/INVARIANTS.md#inv-142)
- **Backup mode picker uses dual visual encoding** → [INV-143](docs/INVARIANTS.md#inv-143)
- **Backup history persists in `localStorage["foldex.backups"]`** → [INV-144](docs/INVARIANTS.md#inv-144)
- **The signed-in user is reachable everywhere via the topbar avatar menu (`UserMenu`)** → [INV-145](docs/INVARIANTS.md#inv-145)
- **Everything a user manages about THEMSELVES is one page, and the bands are not cards** → [INV-146](docs/INVARIANTS.md#inv-146)
- **Changing a password while signed in goes through `POST /api/auth/password/change`, and the CURRENT password is the whole step-up** → [INV-147](docs/INVARIANTS.md#inv-147)
- **The settings hub is the single consolidated settings/administration surface** → [INV-148](docs/INVARIANTS.md#inv-148)
- **Every password input in the tree is `PasswordInput`, and the reveal toggle's three details are the contract** → [INV-149](docs/INVARIANTS.md#inv-149)
- **`.fx-auth-input` is `box-sizing: border-box`, explicitly** → [INV-150](docs/INVARIANTS.md#inv-150)
- **A row action shows its LABEL, not only its glyph** → [INV-151](docs/INVARIANTS.md#inv-151)
- **"Remember my e-mail" stores the ADDRESS and nothing else, writes only after the credentials are ACCEPTED, and unticking ERASES** → [INV-152](docs/INVARIANTS.md#inv-152)
- **The confirmation modal's card needs BOTH `fx-confirm` AND `fx-modal`** → [INV-153](docs/INVARIANTS.md#inv-153)
- **A card that is a `<button>` MUST be styled through an element-qualified selector** → [INV-154](docs/INVARIANTS.md#inv-154) | guard: `scripts/test-css-button-reset.mjs`, `scripts/test-nginx-headers.sh`
  ↳ Shipou 4×: hub "sem estilo", segmented sem estado ativo, `.fx-btn`/`.fx-btn-danger` como texto puro.
- **Every `*attemptlimit.Limiter` on `Handler` MUST be in `limiters()`, and a reflective test is what says so** → [INV-155](docs/INVARIANTS.md#inv-155) | guard: `TestSweepLimitersEvictsEveryBucket`
  ↳ Bucket fora do sweep = `fails` só cresce → lockout de 5 min que a conta não fez nada para merecer.
- **Anything portaled to `<body>` leaves `.fx-shell`, and therefore leaves everything the shell establishes** → [INV-156](docs/INVARIANTS.md#inv-156) | guard: `scripts/test-css-portal-menu.mjs`
  ↳ O user menu rendeu com chrome nativo, translúcido sobre o conteúdo; `.fx-overlay` é o mesmo defeito sem portal.
- **An IDENTIFIER is not a preference, and the account page splits on that line** → [INV-157](docs/INVARIANTS.md#inv-157)
- **Every account panel is ONE card shape, and every message it says back is ONE component** → [INV-158](docs/INVARIANTS.md#inv-158)
- **A class in the markup with no rule behind it is a broken screen, and `scripts/test-css-orphan-classes.mjs` is what refuses it** → [INV-159](docs/INVARIANTS.md#inv-159) | guard: `scripts/test-css-orphan-classes.mjs`
  ↳ Seis ocorrências antes do guard; TS, jsdom e `css:false` não enxergam nenhuma.
- **`.fx-auth` is an OVERLAY, and only a signed-out screen may wear it** → [INV-160](docs/INVARIANTS.md#inv-160) | guard: `scripts/test-css-auth-overlay.mjs`, `TwoFactorSection.test.tsx`
  ↳ Reescrito de inline para classe, virou folha opaca sobre o card — shipou com flicker.
- **Every signed-OUT screen carries a flag row, and it is mounted on `AuthShell`, never per screen** → [INV-161](docs/INVARIANTS.md#inv-161)
- **Locale picker lives in the topbar** → [INV-162](docs/INVARIANTS.md#inv-162)
- **Monitored / unseen-change UI** → [INV-163](docs/INVARIANTS.md#inv-163)
- **Push subscription UI is a bell in the Topbar** → [INV-164](docs/INVARIANTS.md#inv-164)
- **Mobile responsiveness** → [INV-165](docs/INVARIANTS.md#inv-165)
- **Uma troca de tela DENTRO de um fluxo nunca apaga a tela que já está no vidro** → [INV-174](docs/INVARIANTS.md#inv-174) | guard: `AuthGate.transition.test.tsx`
  ↳ Login → pedido do OTP piscava a viewport inteira: o fallback do `Suspense` é uma sobreposição de tela cheia. A linha não é "token na URL", é "havia algo pintado?" — a recuperação de senha entra por `useState` local e tinha o mesmo defeito.
- **A password an administrator installs is either CONFIRMED or GENERATED, never typed once** → [INV-166](docs/INVARIANTS.md#inv-166) | guard: `CreateUserDialog.test.tsx`, `generatePassword.test.ts`
- **Papéis e permissões is a GRID, and every cell it cannot offer says why** → [INV-168](docs/INVARIANTS.md#inv-168) | guard: `admin.test.tsx`

## 6. Definition of Done — every change must check all boxes

Before announcing "done", verify each. If any fails, the change is not done.

- [ ] Code compiles cleanly (`go build ./...`, `bun run build`).
- [ ] `go vet ./...` is silent.
- [ ] `bun run typecheck` is silent.
- [ ] Tests added for new code paths (success + at least one error path).
- [ ] Existing tests still pass (`make test-integration` for backend, `bun run test` for web).
- [ ] Coverage ≥ 85% (`make coverage-backend`, `bun run coverage`).
- [ ] Docs updated per §3 matrix.
- [ ] **`README.md` reviewed and updated** (and `README.pt-BR.md` mirror) — see §3. Any user-visible/stack/feature change MUST land in the README in the same change; re-read the touched sections to confirm nothing went stale.
- [ ] Versions still on latest stable per §1.
- [ ] Invariants in §4 and §5 not violated.
- [ ] If a migration was added: applied to the running Postgres and backend recompiled to use the new schema.
- [ ] User-visible UI changes manually validated in a real browser when behavior changes (not just type-check).
- [ ] **Post-implementation agent sweep run** — see §9. Mandatory for every implementation task. **5 agents** (Code Review, Code Quality, Test Quality, Performance, Security) in parallel — never serialize, never skip "because the change is small."
- [ ] **`graphify update .` run after any code change** — keeps `graphify-out/` in sync with the AST. Free (no API cost). Skipping means future codebase queries return stale results.
- [ ] **Semver bump shipped** — see §6.2. `:latest` is not a release; only a `vX.Y.Z` tag is.

### 6.1 Pre-push gate — MANDATORY before ANY commit / push / PR

Before `git commit` / `git push` / `gh pr create`, run the **exact** CI commands locally and confirm green. NEVER push relying on "the CI will catch it" — wastes minutes per round-trip AND consumes GitHub Actions billing.

If the change touches `.github/workflows/*.yml`, run the **new** commands locally (not what the workflow used to run). Coverage remains a one-pass gate: backend generates `coverage.out` once and checks that artifact; frontend lets Vitest enforce its configured thresholds during its single coverage run.

```bash
# Backend
( cd backend && make fmt-check && go vet ./... && make coverage-run && make coverage-check )
# Frontend
( cd web && bun run typecheck && bun run coverage )
# Guards CI runs that no unit suite can (see §5): the CSS cascade check,
# preceded by its own fixture self-test
node scripts/test-css-button-reset.mjs --self-test --good
node scripts/test-css-button-reset.mjs
```

If the workflow file itself changed, also `grep -E '^\s+run:' .github/workflows/ci.yml` and execute each `run:` line locally. Exception: secrets-gated or matrix-arm64-specific steps — document in PR description and ask the user to confirm CI is acceptable before merge.

### 6.2 Version bump — MANDATORY after every merge to main

Every merge ships code; every shipment gets a version. `:latest` keeps moving but **a moving tag is not a release** — operators can't pin to it, rollbacks have nothing to roll back to, regressions can't be bisected without `vX.Y.Z` tags.

| Merged work | Command | Example |
|---|---|---|
| feat (backwards-compat) | `make release-minor` | 1.0.8 → 1.1.0 |
| fix / chore / ci / docs | `make release-patch` | 1.0.8 → 1.0.9 |
| breaking API/schema | `make release-major` | 1.0.8 → 2.0.0 |
| mixed (feat + fix same window) | `make release-minor` (features dominate) | |

`make release-X` runs `scripts/release.sh` (refuses dirty tree / off-main), atomically bumps `web/package.json`, `extension/manifest.json`, and the backend/web `${FOLDEX_VERSION:-X.Y.Z}` defaults in `docker-compose.yml`, then commits them together. Push main, then manually dispatch `release.yml` from `main` with target `vX.Y.Z`; the validated workflow creates the tag and publishes `:X.Y.Z`, `:X.Y`, `:X`, `:sha-…`, `:latest`. A full 40-character SHA target publishes only `:sha-…` + `:latest`. **The current workflow has no tag-push trigger.** A manually pushed historical tag may still select the workflow file stored in that old commit, so Docker credentials MUST exist only as secrets of the GitHub environment named `release`; delete repository-level copies. Historical jobs do not declare that environment and receive no credentials. Configure required reviewers on `release`; no current publisher starts before approval. **A concorrência do workflow é por ALVO** (`${{ github.workflow }}-${{ inputs.target }}`), não global: com um grupo único, um release que ninguém aprova segura a fila enquanto espera — aconteceu, travou dois dias e cancelou sete dispatches sem publicar, e o sintoma é um run em `pending` com zero jobs, que parece falta de runner. O que o grupo global protegia (duas publicações aprovadas disputando `:latest` no `imagetools create`) fica no job `publish-manifest`, chaveado **por imagem** (`${{ github.workflow }}-manifest-${{ matrix.image.name }}`) — um grupo só para o job inteiro faz as três células do matrix (backend, web, backup-agent) cancelarem uma à outra. `docker/metadata-action` strips the leading `v`, so image semver tags carry NO `v` (pin `FOLDEX_VERSION=X.Y.Z`).

After the bump, surface the new pin to the user: `FOLDEX_VERSION=1.2.0` in `.env` (no `v` — Docker image tags drop it even though the git tag is `v1.2.0`).

If the user explicitly opts out for the current session ("don't bump yet, batching the next 3 PRs"), record the deferral in the session log and resume the policy on the next merge. Default is bump-every-merge — silence is not opt-out.

## 7. Style choices — the project's defaults

- **Backend:** Chi router, pgx + pgxpool, slog. No ORMs, no global state, no service locators.
- **Layering exception (intentional):** primary CRUD packages (`links`, `notes`, `folders`, `tags`, `entries`, `stats`, `settings`) are **handler → repository** with no intermediate service type. Multi-step orchestration that already exists as a dedicated package (`backup.Service`, `importer` apply path, `preview.Worker`) stays there. Do not introduce a service layer for single-repo CRUD — it adds indirection without a second consumer. Prefer extracting a shared helper (`pkg/*`, `tags.SetEntityTags`) over a one-off service.
- **Frontend:** Plain React (no MUI in render). TanStack Query for server state, no Redux. axios as HTTP client. `react-hotkeys-hook` for shortcuts. **i18n via `react-i18next`** — every visible string through `t('key')`, mirrored across `en/pt/es`.
- **A migration that has been APPLIED anywhere is frozen — edit it and you get silent drift, not an error.** `schema_migrations` records only a NUMBER: once version 37 is recorded, `migrate up` has nothing left to do, so anything added to `000037_*.up.sql` afterwards never reaches that database. Nothing reports it — the file and the schema simply disagree, and the disagreement surfaces later as a query against a column or index that exists in the repo and not in the instance. **No code reviewer can catch this**, human or otherwise: the defect is not in any file, it is a mismatch between a file and a running database, and the only record of "this was already applied here" lives in whoever applied it. It happened on this repo's own dev instance with `email_change_user_idx`. The rule is therefore mechanical: **a migration is editable only until the first `migrate up` that touches a database you did not create for that command** — after that, add the next number. During development against a throwaway database, re-apply with `down 1` + `up` and VERIFY the object exists (`pg_indexes`, `information_schema.columns`) rather than trusting the exit code.
- **Migrations:** `golang-migrate`, `000NNN_*.up/down.sql` only. Each migration reversible (real `.down.sql` or explicit `SELECT 1;` with comment).
- **Errors:** uniform JSON envelope `{ "error": { "code", "message" } }`. Repositories (including `repository_system.go`) return only transport-agnostic semantic errors (`internal/pkg/domainerr` or package-local sentinels); handlers own status/code/message mapping and write through `httperr.Write`. `internal/security.TestRepositoriesDoNotImportHTTPDelivery` enforces that no production `repository*.go` imports `net/http` or `internal/pkg/httperr`. Never leak `pgx` errors to clients.
- **Logs:** structured (slog JSON). No `fmt.Println`.
- **Comments:** only when *why* is non-obvious. No "what" comments, no task references, no commit ids.
- **Web deps:** `bun.lock` is authoritative; run `bun audit` (not `npm audit` — no package-lock). Pin policy: exact versions for MUI + TipTap (editor surface is brittle across minors); caret (`^`) for everything else with lockfile as source of truth. Use `overrides` for transitive CVE pins when upstream is slow.

## 8. Architecture in one paragraph

Três projetos docker-compose na rede `foldex`: `docker-compose.db.yml` (Postgres), `docker-compose.services.yml` (RustFS + Postgres alternativo), `docker-compose.yml` (backend Go/Chi `:9089`, web nginx `:9088`/`:9444`, `mailer` sob `COMPOSE_PROFILES=amqp`, volume `foldex-data`). Preview/change-check/push rodam in-process no backend. Schema, projeções e detalhe: `docs/ARCHITECTURE.md` + `graphify query`.

## 9. Post-implementation agent sweep — MANDATORY for every change

Before declaring any implementation task done (and before opening a PR), spawn the **five agents** below **in parallel** via the `Agent` tool and surface every HIGH finding inline. Skipping the sweep is not allowed — it is part of the Definition of Done in §6.

**The five agents** (always all five, parallel, single tool-use block), split by concern — Code Review owns *"correct & coherent?"*, Code Quality owns *"clean & maintainable?"*; never merge or skip one:

1. **Code Review** — architectural coherence vs §4/§5, React/backend idiomaticity, CI correctness.
2. **Code Quality** — dirty code, duplication, comment hygiene (§7), cyclomatic/cognitive complexity, clean-architecture/layering.
3. **Test Quality** — new paths tested (positive/negative/edge), missing cases, antipatterns, coverage gap.
4. **Performance** — re-render storms, memoization, debounce, network waste, bundle, SQL N+1 / missing index.
5. **Security** — XSS / DoS / secret-leak / injection / supply-chain, runtime AND CI.

**Canonical prompts (with each agent's exact "does NOT review" scope) live in [`AGENTS.md`](./AGENTS.md)** — copy verbatim, substitute only the session-scope placeholder.

**Workflow:**

1. After typecheck + tests + coverage pass, call `Agent(...)` five times in one tool-use block — one per agent — with `run_in_background: true` and the session scope filled in.
2. Continue with docs / commit prep / `graphify update .` while they run; harness notifies on completion. Do NOT sleep or poll.
3. When each agent reports back, surface findings to the user. **Treat every HIGH as a blocker** — fix in this session, then re-run the relevant agent against the patched diff. MEDIUM and LOW go to the PR description (or get fixed if cheap).
4. Only declare done after the five reports are visible AND every HIGH is resolved AND `graphify update .` completed.

The sweep is the safety net for changes that *look* small — that's exactly when it's skipped and exactly when it shouldn't be.

---

> Whenever this file conflicts with another instruction in the project (README, ARCHITECTURE), this file wins — update the other doc.
