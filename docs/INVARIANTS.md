# INVARIANTS — o texto longo

> Referência sob demanda. `CLAUDE.md` carrega a REGRA de cada item em uma linha; aqui fica o **porquê**, a consequência observada e o detalhe de implementação — verbatim, sem perda.
> Cada item tem um id estável `INV-NNN`. Nunca renumere: acrescente no fim.

## Dados e comportamento (CLAUDE.md §4)

<a id="inv-001"></a>
### INV-001 — Every content row has an owner, and identity travels as an explicit parameter (ADR-30).

*Guards:* `TestNoUnscopedTenantQueries`

`link`, `note`, `folder`, `tag` and `push_subscription` all carry `user_id NOT NULL` since mig 000017. **Every repository method takes `uid authctx.UserID` right after `ctx`**, and `user_id` is the FIRST predicate in the WHERE so the `(user_id, …)` composite indexes lead. `authctx.UserID` is a distinct type, not `int64`, so swapped arguments are a compile error rather than a leak. This was chosen over Postgres RLS precisely because RLS **fails into emptiness** — a forgotten `SET LOCAL` returns zero rows, indistinguishable from "user has no data" — while an omitted parameter fails the build. Queries that legitimately run unscoped (public `/go/` and `/n/`, the preview and change-check sweeps) live in **`repository_system.go` with a `System*` prefix**; `internal/security`'s `TestNoUnscopedTenantQueries` walks every SQL literal in the tree and fails on a new one anywhere else.

<a id="inv-002"></a>
### INV-002 — The TOTP seed is ENCRYPTED, never hashed — and its key is not regenerable.

Verifying a code needs the seed itself, so hashing is impossible; a plaintext base32 seed in a `pg_dump` is a permanent 2FA bypass. `totp_secret.secret_{ciphertext,nonce}` hold AES-256-GCM output from `internal/pkg/secrets.Cipher`, keyed by `AUTH_ENCRYPTION_KEY` via `internal/pkg/keyfile`. GCM rather than CTR for the **authentication tag**: without it, write access to the database is a seed-substitution attack, and the victim sees only "my authenticator stopped working". `keyfile.Load` is called with **`AllowEphemeral: false`** for this key and `true` for `FOLDER_UNLOCK_KEY` — the distinction is the whole reason the package exists. Losing the folder key means users re-enter a folder password; losing this one makes every seed undecryptable and locks every 2FA user out permanently, so the backend **refuses to boot** rather than mint a session-only key.

<a id="inv-003"></a>
### INV-003 — A TOTP code is spent when it is used — the replay guard is a CONDITIONAL UPDATE, not a Go comparison.

`verifyTOTP` returns the time-step COUNTER it matched (a bare valid/invalid answer would leave a code reusable for the rest of its own 30-second window), and repository transactions write it under `WHERE last_used_counter IS NULL OR last_used_counter < $2` plus the exact `secret_ciphertext` + `secret_nonce` that produced the proof. Two requests presenting the same code at the same instant would both pass a read-compare-write; one of them is the attacker. Proof consumption commits in the SAME transaction as its protected action: challenge consumption + session issuance, enrollment confirmation + recovery-code creation (+ mandatory-login session), set-password, TOTP disable, or recovery-code regeneration. A later write failure therefore restores the proof instead of burning a valid credential for an operation that never happened. Pending enrollment also stores `enrollment_token_version`; confirmation locks `app_user` and requires that originating credential epoch. Password/reset/status changes, session revocation and seed replacement therefore fail closed. Confirming records the counter too, so the enrollment code is not still spendable afterwards. Skew is ±1 step.

<a id="inv-004"></a>
### INV-004 — A six-digit code and a recovery code are told apart by SEPARATOR-STRIPPED LENGTH, never by digit count.

*Guards:* `TestRecoveryCode_WithSixDigitsIsNotMistakenForATOTPCode`

Recovery codes are 16 symbols from a 32-character alphabet (10 of them digits), so roughly **18% contain exactly six digits**. A discriminator that filters to digits and asks "is it six long?" routes those to the TOTP path where they can never match — the holder simply cannot redeem that code. `numericOTP` compacts only whitespace/hyphens and requires exactly 6 remaining, all digits. Locked by `TestRecoveryCode_WithSixDigitsIsNotMistakenForATOTPCode`, which CONSTRUCTS the case rather than depending on randomness.

<a id="inv-005"></a>
### INV-005 — One channel must never satisfy both factors.

*Guards:* `TestReset_MailboxAloneCannotSatisfyBothFactors`, `TestEmailOTP_IsNotOfferedWhenMailOnlyGoesToTheLog`

A password reset proves the FIRST factor only, so a 2FA account diverts into the same challenge login uses — but the e-mail OTP fallback would then mail a code to the SAME address the reset link arrived at, closing both steps on one channel. `auth_challenge.mailbox_already_proven` (mig 000019) marks reset-issued challenges and `emailFactorAvailable` refuses the e-mail factor for them; only an authenticator or recovery code finishes it. The same function requires `MAIL_DRIVER=smtp`, because the `log` driver prints the body to stdout — a deliberate trade for INVITE links (the log is the mailbox on an instance with no SMTP) but not for a second factor, which anyone with the docker group would then read. Locked by `TestReset_MailboxAloneCannotSatisfyBothFactors` and `TestEmailOTP_IsNotOfferedWhenMailOnlyGoesToTheLog`.

<a id="inv-006"></a>
### INV-006 — Credentials are redacted at the ROOT log handler, not at each call site.

*Guards:* `TestRedactionListMatchesTheDocumentedSet`

`internal/pkg/logsafe.RedactHandler` wraps the base `slog.Handler` in `main.go` and blanks the value of any attribute whose key names a credential (`password`, `code`, `recovery_code`, `token`, `access_token`, `refresh_token`, `csrf_token`, `pre_auth`, `secret`, `client_secret`, `code_verifier`, `state`, `sub`, `email`, `recipient`, `cookie`, `api_token`, `temporary_password`, …), matching case-insensitively and on the last segment of a grouped key. It also redacts through `WithAttrs` — `logger.With("token", raw)` stores the attribute once and replays it on every record, so a handler that only cleaned `Handle` would leak it repeatedly while passing the obvious test — and resolves `LogValuer` first, so a type rendering itself as a group of credentials cannot slip past the key check. **The guarantee is key-based, and therefore narrower than it looks**: the record MESSAGE and a struct logged whole (`slog.Any("input", loginInput{…})`, whose fields the handler never sees as attributes) both pass through untouched, and stay the call site's responsibility. `code` is the one entry that over-redacts — it names an OTP as naturally as an error code — so it stays and the one benign call site was renamed to `s3_error_code`. The list is locked from BOTH sides by `TestRedactionListMatchesTheDocumentedSet`: a test that merely ranged over the map would delete its own case along with the key. Nothing logs a secret today; this exists so the NEXT log line, added in a hurry during an incident, cannot make one permanent. Same reasoning as `SweepTouch` over `forgetTouch`: bounded by construction beats bounded by discipline.

<a id="inv-007"></a>
### INV-007 — `X-Forwarded-For` is honoured ONLY from a configured proxy.

`server.trustedProxyRealIP` replaces chi's `middleware.RealIP`, which believes the header unconditionally — correct behind nginx, forgeable on a direct bind, where it makes every IP-keyed rate-limit bucket decorative. With `TRUSTED_PROXY_IPS` empty the header is ignored entirely, because a spoofable client address is worse than a coarse one: it lets an attacker both evade their own bucket and pin the cost on someone else. **`docker-compose.yml` must therefore set it** — the shipped stack puts nginx in front, and an empty list there attributes every request to the proxy, collapsing the 20-failure login bucket into one global budget that any single attacker can exhaust for everyone. The boot warning fires when the list is empty on a non-loopback bind. Hops are parsed tolerantly (`ip:port`, bracketed IPv6, multiple header lines via `Header.Values`) and capped at 32, because rejecting a shape a real proxy emits silently falls back to the proxy's own address — the very failure the middleware exists to prevent. The chain is walked RIGHT to left, skipping trusted hops — the leftmost entry is whatever the client sent and stays attacker-controlled with more than one proxy.

<a id="inv-008"></a>
### INV-008 — E-mail confirmation is a LINK, not a code, and its endpoint is unauthenticated.

`POST /api/auth/email/verify` takes a 256-bit token and resolves by hash alone with no `user_id` — safe only because the token IS the identifier; a six-digit code looked up that way would be guessable across the whole user base at once. Unauthenticated because the link is followed from a mail client, often on a device that never signed in. Every failure — unknown, expired, spent — is the same `404 verify_invalid`. `/email/resend` is session-authenticated and mails to the CALLER's own address, never one from the request, so it cannot be turned into a relay. Spending the token and marking the address verified are **one statement** (a CTE): split in two, a failure between them burns the token while leaving the address unverified, and the only way to get another is the session-gated resend — so someone following the link on a device that never signed in would be stuck. Lookup is by hash alone, so `email_otp` carries a partial index on `code_hash` (mig 000020); without it every click sequential-scanned a table an unauthenticated caller can grow.

<a id="inv-009"></a>
### INV-009 — `Optional` enforces CSRF on unsafe verbs.

It resolves a principal without requiring one, and PR3 mounted two POSTs on it. Skipping the check would make "optional authentication" a way to mount an unsafe verb outside CSRF protection entirely — the browser attaches the session cookie to a cross-site POST regardless. Safe methods are untouched, which is why `/me` is unaffected.

<a id="inv-010"></a>
### INV-010 — Second-factor budgets live in the DATABASE, not in `attemptlimit`

— except on the session-authenticated step-up paths (`/2fa/totp/disable`, `/2fa/recovery-codes/regenerate`), which have no challenge and therefore get an in-memory per-user limiter instead. Without it nothing bounded TOTP guessing there at all. `auth_challenge.attempts` (5) and `.sends` (3, plus a 60 s cooldown enforced inside the same UPDATE) are columns because §4's folder-unlock rule — "restart clears the state, acceptable" — does not transfer: bcrypt's cost is the folder's real floor, while verifying a 6-digit code is one hash compare. A restart that zeroed the budget would hand an attacker fresh guesses for the price of crashing the process. The attempt is charged BEFORE the code is checked, so a cancelled request costs the attacker nothing only if it also gains nothing.

<a id="inv-011"></a>
### INV-011 — Every pre-auth challenge, password-reset link and session-authenticated credential proof is bound to the live credential epoch.

Migration 000025 adds `auth_challenge.token_version` plus `totp_secret.enrollment_token_version` and `enrollment_session_id`; migration 000028 adds `password_reset.token_version`. Pre-migration NULL rows fail closed. Every new challenge, pending enrollment, ordinary reset and administrator recovery copies the live `app_user.token_version`; Settings enrollment also stores the exact session that supplied the password. Resolve, attempt/send reservation, consumption, TOTP enrollment writes, reset spending/password mutation and session issuance validate the same live epoch under the `app_user` row lock. Session-authenticated set-password, TOTP disable/regeneration and OAuth unlink additionally revalidate the exact live session at the write boundary. Password change/reset, logout-all, role/status mutation and administrator revocation therefore kill stale proofs and reset links before they can enroll a factor, change credentials or mint a session. Credential-set mutations bump `token_version` and revoke other sessions in their own transaction; OAuth conversion conditionally consumes the exact live challenge before linking identity/retiring password, while OAuth unlink validates password, removes identity, bumps epoch and revokes other sessions atomically.

<a id="inv-012"></a>
### INV-012 — A username is OPTIONAL, and its ban on `@` is what keeps two namespaces apart (ADR-41, mig 000037).

`app_user.username`/`username_normalized` are nullable with a PARTIAL unique index; there is no backfill, because deriving `valmir.justo` from `valmir.justo@…` publishes half a mailbox under a name its owner never chose. Login resolves ONE identifier against both columns in the SAME statement (`email_normalized = $1 OR username_normalized = $1`) — a branch on "does it look like an e-mail?" would take two different amounts of time, which is precisely the enumeration oracle bcrypt-always-runs, the single 401 and the 250 ms floor exist to close. **So a username shaped like an address would sit in everyone's mailbox namespace**: claim `victim@example.com` and that account's password attempts arrive at yours. Refused by `app_user_username_shape` in the database AND by `NormalizeUsername` in the handler, because a handler is one code path and the next one to write that column would have to remember. Reserved names (`admin`, `root`, `support`, …) are refused too — not a routing conflict, a social-engineering prop. **The login rate-limit key is RESOLVED before it is charged** (`Repository.loginBucketKey`): keyed by the string typed, an attacker alternates an account's two names and gets double the budget while the per-account cap still reads five; an identifier that resolves to nothing keeps its own key, which is what preserves the increment for names that do not exist.

<a id="inv-013"></a>
### INV-013 — There is a username availability probe and there is deliberately NO e-mail one outside administration.

*Guards:* `TestAvailability_EmailProbeIsMountedOnlyUnderAdmin`

`GET /api/auth/username-available` answers a boolean about ANOTHER account's identifier, which makes it an enumeration oracle by construction — and a cheap one, since it needs no password. Three things keep it acceptable and none is optional: it is **session-only** (an anonymous caller cannot reach it, so login's single 401, always-run bcrypt and 250 ms floor are untouched); it answers about **usernames and never e-mail** — an address is also a mailbox and exists outside this instance, so confirming one is taken says *this person has an account here*, while a username exists only here and says only *somebody here uses that handle*; and it is **capped per user** (`availabilityUser`, 60 per 5 minutes — set for a person TYPING against a debounced field, which costs a script four hundred lookups an hour instead of thousands a second). The budget is charged BEFORE the lookup and **charged for shape-refused values too**, or a script probes for free by appending a character the validator rejects — the same reasoning that makes the login bucket increment for addresses that do not exist. An EMPTY probe is free: it looks nothing up and reveals nothing, so charging would only punish someone clearing the field. The caller's own current username is available to them, or the form reports "taken" about the name its owner is looking at. **The e-mail counterpart lives ONLY under `/api/admin`**, and that placement is its whole safety argument: past `RequireAdmin` the caller can already list every account with its address, so it discloses nothing they could not read directly — which is also why it is uncapped. It must never be mirrored onto a route an ordinary session or an anonymous caller can reach; the e-mail-CHANGE flow keeps its answer at submit time, where a password is the cost of each guess. **That placement is now STRUCTURAL, not documented** — `TestAvailability_EmailProbeIsMountedOnlyUnderAdmin` walks the route tree and requires every `email-available` under `/api/admin/` and every `username-available` under `/api/auth/`, with a non-empty check on both so a rename cannot make the guard vacuous. Mirroring it "for symmetry with the username row" is this feature's most likely regression. **The cost the scoping argument does NOT price**, and which belongs beside its three justifications: usernames are optional and no other surface lets one account learn another's handle, so this is a genuinely new disclosure to any session including a `viewer` — and a handle plus five wrong passwords parks that account outside password login for 15 minutes, renewably (`loginByEmail`, keyed by the RESOLVED identifier). The lockout is pre-existing and previously needed addresses; what changed is that the target list became obtainable. Two things bound it: the probe CONFIRMS a guess rather than enumerating, and `loginByIP` caps it at roughly four victims per IP per window. **The budget is charged even when the client ABORTS**, deliberately: charging after the lookup would let a caller who hangs up before the answer pay nothing, which is the whole budget, since nothing forces an attacker to read the body to learn from the connection's timing. A **pending** `email_change` counts as taken there, because the unique index guards only the live column and an address someone is already moving to would otherwise pass the check and lose the race at their own confirmation. The probe **gates the submit on a refusal only, never while in flight, and never on a failed check** — blocking during the debounce leaves the button dead for 450 ms with no visible cause, a click that races the check reaches the server which refuses it anyway, and the copy on a failed probe says *you can still save*, which the code has to mean. Both submit affordances obey the same gate: Enter was gated only on "changed and not saving" while the button also checked the refusal, so one field offered two ways in that disagreed about whether submission was possible. **`reason: "pending"` rides an AVAILABLE answer, and that split is load-bearing**: `AdminCreateUser` conflicts only on `app_user`, so reporting a pending `email_change` as *taken* greys out a create the server would accept — §5's "a screen hiding a button the server would allow reads as a missing feature", with no route forward, because the user list shows no such account either. A pending row may also expire or have its credential epoch killed, freeing the address again. So it warns and the administrator decides. **The probe is injected as a function, not a URL** (`Probe` in `useAvailability`), so the route and response type live in `api/*` like every other call — and a component test that mocks it cannot drift from the endpoint the backend mounts. **It is held in a ref and kept OUT of the effect deps**: in the deps, a caller passing an inline arrow gets a new identity per render, the effect re-runs, that sets state, and the loop ends in an out-of-memory kill rather than a warning. The abort guard belongs on the **success arm as well as the catch** — `abort()` is a no-op on a settled promise, so a response landing just before the next keystroke otherwise writes an answer about the previous value, and since the gate derives from that state the other ordering clears a real refusal and re-enables Save.

<a id="inv-014"></a>
### INV-014 — The account's e-mail moves only after the NEW address confirms, and the OLD one is told without a link (ADR-41, mig 000037).

`POST /api/auth/email/change` proves the current password, checks the address is free and writes an `email_change` row plus TWO outbox messages; it changes nothing. Writing the address straight in would make a typo the login AND the recovery channel, with the warning going to the address that was typed wrong — the same property `AdminCreateUser` protects by creating accounts unverified. The notice to the current address uses `chrome.shape_notice`/`text.shape_notice`, which have **no URL slot on either arm**: its reader may be someone whose account is being taken by a person who already holds their session, and "click here to stop it" is the forgery's own sentence (same rule as `session_revoked`). **Everything is re-checked at COMMIT under the `app_user` row lock** — the credential epoch still matches (a password change, reset or logout-all between request and click kills the pending move), the address is still free (the unique index is the only defense against someone claiming it in the interval), the row is still unspent — and spending the token and moving the address are ONE statement, or a failure between them burns the token while the address stays put. Consuming bumps `token_version` and revokes EVERY session under the reason `email_changed`, which is its own value because reusing `password_changed` would put a false sentence in a trail ADR-34 makes outlive the account. `POST /api/auth/email-change/confirm` is UNAUTHENTICATED, like `/email/verify` and for a stronger reason: the link arrives in the mailbox the account is moving TO. One 404 for every failure except `email_taken` (409), told apart deliberately — its holder proved control of the destination mailbox and can fix it by choosing another address. Only one live request per account (`email_change_one_pending`), so an address typed wrong twice cannot leave two mailboxes each able to take the account.

<a id="inv-015"></a>
### INV-015 — A Google `sub` is the ONLY key that resolves an OAuth login; a matching e-mail opens CONVERSION, never a session (ADR-31).

*Guards:* `TestOAuth_EmailMatchAloneNeverIssuesASession`

`user_identity.subject` is what `UserByIdentity` looks up. E-mail never resolves a login: a Google account's address is changeable by its owner, and matching on it would let that change move a foldex account. When the subject is unknown but the address matches an existing PASSWORD account, the callback opens a `convert_google` challenge and answers `convert` — it does not sign anyone in and does not refuse. `POST /oauth/google/convert` then demands the account's **current password**, and only then links the identity, sets `password_hash = NULL` and revokes every session, in one transaction. Deliberately stricter than password reset, which does hand an account to whoever controls the mailbox: conversion is a migration the owner performs, so "I forgot my password, let me use Google" does not work. Unknown address → `not_linked`, and **auto-provisioning is OFF unless an owner explicitly turns it on** (ADR-35 revokes the unconditional invite-only rule; the safeguards that make that acceptable are in the policy bullet below — the answer stays `not_linked` in every refused case either way). A non-active account, and an UNVERIFIED Google address, answer the SAME `not_linked` an unknown one gets — the verified check runs BEFORE the e-mail lookup, because a distinct answer only when the address matched would turn the callback into an existence oracle. Locked by `TestOAuth_EmailMatchAloneNeverIssuesASession`.

<a id="inv-016"></a>
### INV-016 — OAuth never skips the second factor.

*Guards:* `TestOAuth_ConvertStillRequiresTheSecondFactor`

Every OAuth exit — linked login, conversion, invite acceptance — funnels through `secondFactorPurpose`/`beginChallenge` exactly as a password login does. With TOTP mandatory for admins, an OAuth path that minted a session directly would make "sign in with Google" strictly weaker than the password it replaces. Locked by `TestOAuth_ConvertStillRequiresTheSecondFactor` and `..._LinkedLoginDoesNotBypassTOTP`.

<a id="inv-017"></a>
### INV-017 — The Google subject travels on the CHALLENGE ROW, never through the browser.

`auth_challenge.oauth_{provider,subject,email}` (mig 000021) is written by the callback and read by the convert POST. If the request could name a subject, someone who proved a password could attach a *different* Google account than the one authenticated. Two things hold it: the DTO refuses unknown fields (`DecodeJSON` uses `DisallowUnknownFields`), and the conversion reads the row regardless.

<a id="inv-018"></a>
### INV-018 — Linking an OAuth identity requires a fresh credential proof, not only a session.

`GET /oauth/google/start?purpose=link` is refused. Settings first `POST`s the current password plus a current TOTP or single-use recovery code when TOTP is confirmed; CSRF and independent per-user password/second-factor budgets apply before the API returns Google's redirect URL. The `oauth_state` row binds `user_id`, the exact `session_id`, `token_version` and a five-minute `proof_at`. The callback validates that binding before contacting Google and `LinkIdentity` validates it again under locks at the write boundary. Logout, session revocation, password change/reset, another session for the same user, an expired proof and state replay all fail as the same `state_invalid` redirect and create no identity. Password/code remain in the JSON body and never enter a URL or log attribute.

<a id="inv-019"></a>
### INV-019 — `userColumns` is fully qualified with `app_user.`, and that is load-bearing.

`UserByIdentity` selects the projection from `app_user JOIN user_identity`, and `user_identity` has its own `created_at` and `last_login_at`. Unqualified, both are ambiguous and Postgres refuses the whole query — which surfaced as an opaque `server_error` on the Google login path that no single-table test could reach. Never drop the prefix.

<a id="inv-020"></a>
### INV-020 — An ACTIVE account always holds at least one credential, and the DATABASE is what guarantees it.

Migration 000021 adds a `CONSTRAINT TRIGGER ... DEFERRABLE INITIALLY DEFERRED` on `app_user` (INSERT/UPDATE of `status`, `password_hash`) and on `user_identity` (DELETE only — an INSERT can only ever ADD a credential). Deferred because the conversion legitimately violates it *inside* the transaction: nulling the password and inserting the identity are two statements. What must hold is the state at COMMIT. `pending` and `disabled` are outside the rule on purpose — the bootstrap placeholder ships `pending` with no password, and that row is what the setup screen claims. `UnlinkIdentity` re-checks in its own transaction so the UI gets a `409 password_required` instead of a 500.

<a id="inv-021"></a>
### INV-021 — `POST /api/admin/users` is a DECLARED EXCEPTION to the rule below, taken by the instance owner.

*Guards:* `TestAdminCreateUser_`

It creates an account whose first password the administrator types, and it exists because the owner asked for it after the trade was put to them: for the window between creation and the target's first password change, two people know one credential, and no audit entry can tell a sign-in by the account's owner from one by the administrator who typed its password. Everything else in this surface — invitations, reset links, `force-password-reset` — exists to avoid exactly that window and remains the recommended path; the UI says so in the dialog, because the owner accepted the trade knowingly and the next administrator to open it did not. **What was NOT given up:** the configured password floor applies (`AdminHandler.validatePassword`, a METHOD for §4's usual reason — `WithPolicy` wires it, and a nil policy runs the compiled-in floor); the address is created UNVERIFIED, so a typo cannot become a confirmed mailbox nobody controls; the role is refused at the handler AND the repository and can never be `owner`; and the password never reaches the audit trail, which records only the address and the role. Locked by nine integration tests under `TestAdminCreateUser_*`.

<a id="inv-022"></a>
### INV-022 — An administrator never chooses, installs or receives another user's credential

— outside the declared exception above. `POST /api/admin/users/{id}/force-password-reset` is an SMTP-only, user-bound recovery trigger: the single-use token goes only to the target's verified mailbox, the admin receives an empty `202`, and password/session/token epoch stay unchanged until the target consumes it and chooses a password. SMTP failure rolls the token transaction back. Google-only accounts are eligible because the proof is admin authorization plus the verified mailbox; a confirmed TOTP remains required as the second factor, and token consumption atomically sets the hash, bumps `token_version` and revokes existing sessions. The log mail driver is forbidden for this credential.

<a id="inv-023"></a>
### INV-023 — API tokens are scoped to CONTENT, and the scope is enforced by middleware, not by discipline.

`Authorization: Bearer fx_<id>_<secret>`; stored as sha256, resolved by id then compared in constant time; `Via = ViaAPIToken` skips CSRF (no ambient credential to ride on). `Middleware.RejectAPIToken` is mounted on **all of `/api/auth`'s session group, `/api/admin` and `/api/backup`** — a token pasted into an extension's config must not change a password, mint an invite, administer users or download a backup, or it stops being a content credential and becomes the account. On `/api/admin` the ROLE gate runs FIRST: a non-admin gets 404 either way, and only an admin sees the 403 `token_scope`. Disabling an account kills its tokens in the same statement that resolves them.

<a id="inv-024"></a>
### INV-024 — `/go/{42}` and `/n/{42}` are OFF by default (ADR-32).

Both resolve with no session — public share links, no tenant to scope by — and `link.id` is a dense global `BIGSERIAL` shared across accounts, so a numeric path lets anyone walk 1, 2, 3… and enumerate (and click-log) every link and note on the instance. `/n/` is worse: it RENDERS content. `PUBLIC_NUMERIC_IDS=1` re-enables both, for instances with old links already shared. A disabled numeric lookup answers the SAME 404 an unknown slug gets.

<a id="inv-025"></a>
### INV-025 — `totp_enabled` and `email_2fa_enabled` are derived with `EXISTS`, never stored, and `HasSecondFactor()` is their OR (ADR-37, mig 000036).

An account holds a factor exactly when it has a CONFIRMED `totp_secret` or `email_factor` row. A cached boolean would need updating in four places and would disagree with reality the first time one was missed — and the direction of the disagreement decides whether login demands a code the user cannot produce. E-mail is now a factor the account **enrolls**, not a capability the server offers whenever SMTP happens to be configured: `emailFactorAvailable` requires `u.Email2FAEnabled` on top of the `mailbox_already_proven` and `MAIL_DRIVER=smtp` guards, which are unchanged. Enrolling it **mandates recovery codes**, because the reset-link guard deliberately refuses the e-mail method — without them that safety rule becomes a locked door.

<a id="inv-026"></a>
### INV-026 — "May this factor be removed?" is answered in ONE place.

`mayRemoveFactor` is consulted by `/2fa/totp/disable`, `/2fa/email/disable` AND `GET /2fa`, which returns `can_disable_totp` / `can_disable_email` so the settings screen renders from the server's answer instead of re-deriving the policy. Two copies drift in the direction nobody notices: a screen hiding a button the server would allow reads as a missing feature, and one showing a button the server refuses reads as a broken account. Under `admin_second_factor = any` an admin holding BOTH factors may drop one — refusing outright would treat "has two factors" as stricter than "has one". `DisableTOTP` deletes the recovery codes only when **no** factor remains, read under the `app_user` lock the transaction already holds; unconditional, it left an account with an e-mail factor and no lockout exit.

<a id="inv-027"></a>
### INV-027 — A session step-up accepts TOTP, a recovery code or a mailed code, and the proof is VERIFIED without being spent.

The four session-authenticated paths (disable a factor, regenerate recovery codes, set a password, link an OAuth identity) accepted only TOTP, so an account whose sole factor was e-mail could not even set a password. `POST /2fa/email/send` mints a code under its OWN purpose (`step_up_2fa`, mig 000036): separate from `enroll_email_2fa` because one proves a mailbox nobody has accepted yet and the other is an accepted factor presenting itself, and a shared purpose would let the first be spent as the second. The six-digit discriminator **falls through** from TOTP to e-mail rather than chaining `else if` — the two shapes are indistinguishable, and committing to the TOTP branch would make the e-mail factor unusable for exactly the accounts with no authenticator. The e-mail branch requires `Email2FAEnabled`, or mailbox control alone would authorize removing an authenticator. `SecondFactorProof` is consumed by `consumeSecondFactorTx` **inside the transaction** of the operation it authorizes, for all three methods: a recovery code burned by a write that then failed costs the user a way back into their account for an operation that never happened.

<a id="inv-028"></a>
### INV-028 — Recovery codes and six-digit e-mail OTPs use keyed, context-bound digests and remain single-use by conditional UPDATE.

`AUTH_ENCRYPTION_KEY` feeds an HMAC-SHA256 KDF; AES key/nonce material is never reused directly. Separate e-mail/recovery subkeys prevent cross-domain substitution. The MAC input includes a version and purpose; e-mail also binds `user_id`, `challenge_id` and code, while recovery binds `user_id` and the normalized 80-bit code (`XXXX-XXXX-XXXX-XXXX`). `UPDATE ... WHERE user_id = $1 AND code_hash = $2 AND used_at IS NULL` keeps ownership and single-use atomic; the ownership predicate remains mandatory even though the MAC is user-bound. Migration 000023 deletes every legacy unkeyed digest because the plaintext cannot be converted and dual-format acceptance would preserve offline enumeration. Disabling TOTP deletes recovery codes in the same transaction.

<a id="inv-029"></a>
### INV-029 — `POST /api/auth/password/forgot` ALWAYS answers 202, on three channels.

Status/body (one shape for unknown address, disabled account, rate-limited, malformed input), timing (same 250 ms floor login uses), and the INBOX: an account with no password credential receives a "this account signs in with Google" message rather than silence, because silence leaves the mailbox as the oracle. That message carries **no link** — a reset link there would let control of the mailbox alone resurrect a password credential, which is exactly what ADR-31's conversion refuses. Reset runs in ONE transaction: spend the token, set the hash, bump `token_version`, revoke every session.

<a id="inv-030"></a>
### INV-030 — Auth mail is written to a TRANSACTIONAL OUTBOX, in the same transaction as the credential it carries (ADR-36, mig 000034).

This replaces the in-memory dispatcher and its `Reserve/Publish/Release` admission protocol, which bounded the queue but could not survive a restart: a deploy between the commit and the send dropped the message while the reset token — and its 60-second cooldown — stayed, leaving the user waiting for a link that no longer existed anywhere. `auth.Repository`'s credential-minting methods take a `MailDraft` whose `Build(rawToken)` runs INSIDE their transaction, so either both rows exist or neither does; there is no `503 mail_queue_full` any more, because an INSERT cannot fail for capacity. Login send-budget increment, code publication and the message are one transaction, as are invite creation, reset and verification. `mailoutbox.Relay` drains the table with `FOR UPDATE SKIP LOCKED`, settles under a `claim_token` CAS, retries with a backoff column (never a sleeping worker, which would hold its slot), requeues claims abandoned by a dead relay, and settles a permanently broken message (`unknown_template`, `undecryptable_payload`) at once instead of spending six attempts on it. **The payload is AES-256-GCM under a SUBKEY derived from `AUTH_ENCRYPTION_KEY`** (`secrets.NewDerivedCipher`, purpose `foldex/mail-outbox/payload/v1`), never the master key itself — the TOTP seed already encrypts under that one, and the outbox is the only domain whose volume is bounded by nothing (one ciphertext per reset link, sign-in code and invitation, against one per user), so sharing a key would make their distances to GCM's birthday bound a single shared budget. Same reasoning as the per-purpose code-MAC subkeys above. Encrypted at all because a queued row holds a live reset link, `password_reset` stores only a sha256 precisely so a `pg_dump` is not a takeover kit, and the GCM tag is what stops write access to the table from becoming a link-substitution attack. The purpose string is a domain separator and must never change: a new value is a new key, and every queued row becomes undecryptable. `last_error` is a NORMALIZED reason, never the transport's text, because an MTA rejection quotes the envelope back. Authenticated verification resend still coalesces for 60 s. **Administrator force-reset now rides the outbox too (ADR-36 §12.1)**: token and message commit together, so the property that motivated the old synchronous send — *an administrator never installs a credential the target does not receive* — is preserved by DURABILITY rather than by coupling. `503 mail_unavailable` is gone, and a transient SMTP blip no longer denies the administrator an operation they are entitled to perform while discarding the token. The `MAIL_DRIVER=smtp` requirement stays, because the `log` driver would print this credential to stdout. The post-delivery eligibility re-check was REMOVED with it: it defended a window that existed only because the send blocked inside the transaction, and the `app_user` row now stays locked `FOR NO KEY UPDATE` from read to commit.

<a id="inv-031"></a>
### INV-031 — The mail TRANSPORT is pluggable, and only the sink changes (ADR-36 fase B).

`MAIL_TRANSPORT` is `inproc` by default — the relay renders and sends in-process, so a self-hosted binary plus a Postgres keeps durability, retry and backoff, and loses only horizontal scale. `amqp` swaps in `mailoutbox.AMQPSink` and nothing above it moves: the handlers write rows, the relay drains them, and neither learns what a broker is. An unknown value **refuses to boot** rather than falling back, because a silent downgrade keeps mail working while every message an operator expects on the broker is quietly sent by the backend they scaled workers away from; `amqp://` to a NON-loopback host is refused for the same reason `MAIL_INSECURE_SKIP_VERIFY` is (the broker credential would cross the network in clear). **The message on the wire is the SAME sealed ciphertext the row holds** — the broker persists to disk, is routinely shared between projects, and is outside this application's threat model, so it never sees a rendered reset link; only `cmd/mailer` opens it. **Plaintext `amqp://` off-loopback needs `AMQP_ALLOW_PLAINTEXT=1`, and the claim is VERIFIED AT BOTH ENDS.** Default `0` keeps the previous refusal. The flag asserts something about the NETWORK, so it is checked against the network twice: at boot when `AMQP_URL` carries an IP literal, and **at dial against the peer actually reached** — which is the leg that matters, because a HOSTNAME is the majority form (a compose service name included), its resolution belongs to infrastructure the operator may not control, and it can change at any reconnect. `requirePrivatePeer` closes a rejected connection before the AMQP handshake writes the SASL PLAIN credential, and fails closed when the address is not a `*net.TCPAddr` — same two-legged shape as `preview.safeDialer`, for the same reason: a name is not an address. It also covers the literal `net.ParseIP` rejects but the resolver accepts (`3221225985` dials `192.0.2.1`). Permitted ranges are RFC1918, CGNAT (100.64/10 — the upper bound is 127, since 100.128.0.0/9 is allocated PUBLIC space), loopback and link-local, via `internal/pkg/privatenet`. That predicate is deliberately NOT `netpolicy.IsPrivateIP`, which blocks SSRF and so treats the whole IANA special-purpose registry — documentation ranges included — as private: right for a fetcher, wrong for deciding whether a credential may cross a link in clear. **What the relaxation exposes** is the broker credential, the routing metadata, and the RECIPIENT plus TEMPLATE of every message (who is receiving which credential, and when); the body stays sealed. **Broker TLS is configured in the URL, never by a separate variable.** The refusal of plaintext `amqp://` to a remote host points at `amqps://`, and a private broker's chain is trusted through the AMQP URI's own query parameters — `?cacertfile=` (replaces the system roots), `?certfile=`/`?keyfile=` (mTLS), `?server_name_indication=` (SNI) — which `amqp091-go` honours only while `TLSClientConfig` stays nil (`connection.go:324`). Adding an `AMQP_CA_FILE` env var therefore does NOT extend the feature and actively breaks it: a non-nil config makes the library skip `tlsConfigFromURI` wholesale, duplicating `?cacertfile=` while silently disabling mTLS and SNI for anyone already using them. Under Docker these paths resolve INSIDE the container, which is why compose mounts `./certs` at `/etc/foldex/certs` for backend and mailer. A certificate for an IP needs an IP SAN; SNI changes which name is checked, not whether the address is in the certificate. **`cmd/mailer` has NO database credential, by construction** (`config.LoadMailer` exists precisely so it can boot without `DB_URL`): it is the one process that decrypts live credentials, and giving it Postgres too would make compromising it compromise everything. That split is why the retry ladder is **one queue per step** (`.retry.1m`/`.5m`/`.30m`) rather than a per-message TTL — RabbitMQ expires from the HEAD only, so a 30-minute message in front holds back every shorter one behind it — and why the worker REPUBLISHES explicitly instead of nacking, since a nack routes to one fixed key and cannot express an escalating wait. Publisher confirms are mandatory on both the sink and the republish: without them the ack that follows deletes a message the broker never took. **The retry counter is BOUNDED where it is read, not where it is written** (`clampAttempt`, ceiling `1<<20`): it rides a header on that same out-of-threat-model broker, and the worker's `Attempt(headers) + 1` on a crafted `MaxInt64` wraps NEGATIVE — which the give-up test reads as an early attempt, the ladder clamps to its slowest rung, and the `int32` header truncates back to zero, so the message circles forever instead of reaching the dead queue, with no crash and no log. Clamping at the cast alone would be too late, because `attempt >= MaxAttempts` decides the destination first; the function returns `int32` so the bound sits directly above the conversion rather than a call away from it. **`mailoutbox.DeadLetterWatcher` runs in the BACKEND, not the worker**, consuming `foldex.mail.dead` and calling `MarkDead` — handing delivery to a queue moves the truth about it out of the database, and without the watcher every row would read `published` whether the message arrived or died on the last rung. It reads only an id and a normalized reason, both of which travel OUTSIDE the sealed blob, so reporting never requires the key.

<a id="inv-032"></a>
### INV-032 — `${VAR:?...}` in a compose file is a WHOLE-FILE gate, never a per-service one.

Compose interpolates every service before starting any of them, and profiles do not exempt what they exclude — so a colon-form guard on a service the operator does not run refuses the ones they do. This has now bitten twice: `AMQP_URL:?` took down the default stack, and the object store's `RUSTFS_ROOT_SECRET_KEY:?` refused to start the mailer, the backend and the web, none of which read it. The fix both times was structural, not a longer message: the object store left `docker-compose.yml` entirely (it already lived in `docker-compose.services.yml`, and the two copies had drifted), and `make up` decides whether to bring it along from `RUSTFS_ENDPOINT` the same way it already did for `POSTGRES_HOST`. The remaining `RUSTFS_SECRET_KEY` on the backend is `:-`, not `:?`, because `main.go` treats the object store as OPTIONAL and degrades deliberately — a colon form there converts a documented degradation into a hard refusal of everything.

<a id="inv-033"></a>
### INV-033 — A Makefile recipe that names a host path uses `$(CURDIR)`, never `$(PWD)`, and a bind mount whose source is missing is not an error.

*Guards:* `scripts/test-make-migrate-path.sh`

The root Makefile delegates with `$(MAKE) -C backend`, and `make -C` chdirs without updating the PWD *environment variable* — only `CURDIR` follows it. `MIGRATE` was written with `$(PWD)/db/migrations` and therefore resolved to `<root>/db/migrations`, which does not exist; Docker creates a missing bind-mount source as an empty directory rather than refusing, so golang-migrate found zero files and exited **0** with `no change`. The failure is silent AND self-cancelling: `db.CheckSchemaVersion` makes the backend exit 1 against an un-migrated database and tell the operator to run `make migrate-up` — the command that had just done nothing — and that command is README.md's quickstart line. `scripts/test-make-migrate-path.sh` asserts on the EXPANSION (`make -n`) from both entry points, and separately that the directory exists, because "equal from both and wrong from both" would pass a comparison alone. **The same target had a second, independent break: the HOST.** `POSTGRES_HOST=localhost` is supported — compose aliases `localhost` to the host gateway for the backend, so a Postgres on the developer's own machine works — but the migrate CLI runs in a container of its own with no such alias, where `localhost` is the container itself. `make migrate-up` therefore failed with "connection refused" on an instance whose backend was connected and healthy, and README's next instruction is that same command. **`--add-host=localhost:host-gateway` does NOT fix it**: Docker appends to `/etc/hosts` and the pre-existing `127.0.0.1 localhost` line still wins the lookup — verified, the name resolves to `::1` with the alias present. The URL is rewritten instead (`MIGRATE_DB_URL`), which also covers an explicitly overridden `DB_URL` rather than only the one derived from `POSTGRES_HOST`. The guard asserts BOTH halves — a local host becomes `host.docker.internal`, and any other host passes through UNTOUCHED, or a genuinely remote Postgres would be silently redirected at the developer's machine. **It probes with a SYNTHETIC `DB_URL` on the command line and never echoes a DSN**: the first version printed the expansion on failure and put the instance's real Postgres password on the terminal — the defect `logsafe` exists to prevent, in the one file whose job is to read that expansion. The DAST now runs the same `make migrate-up` an operator runs, so a break in the documented path fails in CI instead of on someone's first install.

<a id="inv-034"></a>
### INV-034 — A publisher with no consumer is a WARNING, and a send is a LOG LINE.

Under `MAIL_TRANSPORT=amqp` the two halves of delivery live in different containers, and the `mailer` service carries `profiles: ["amqp"]` — so a plain `docker compose up -d` starts the backend and not the worker. That failure is invisible from every record the system keeps: the publish is routable and confirmed, `mail_outbox` settles as `published`, and the queue quietly fills. It happened on a live instance, and what made it undiagnosable was that the worker logged **nothing** on success, so "mailer ready" read identically after draining a hundred messages and after sitting idle for a day. Two things close it. `Topology.Declare` returns the send queue's `SendQueueState` — the broker reports consumer count for free on every declare — and `AMQPSink` warns when it connects to a queue nobody reads; the count comes from the declare rather than a passive probe, so it costs no round-trip, and the honest limit is that it is a SNAPSHOT taken at connect, not a watch. `mailworker.handle` logs one INFO per successful send, carrying `outbox_id` and `template` and **never the recipient**: `logsafe` redacts the key `email`, not `recipient`, so naming the address there would print it in clear in the one process that also holds a live reset link. `COMPOSE_PROFILES=amqp` in `.env` is what makes the two services agree, and it sits directly above `MAIL_TRANSPORT` in `.env.example` for that reason.

<a id="inv-035"></a>
### INV-035 — Every auth e-mail renders from an embedded template at DELIVERY time, in the recipient's locale.

*Guards:* `TestLinklessMessagesCannotBeGivenALink`, `TestEveryLinkCarryingMessageRendersItsLinkInBothArms`

**The HTML layout is PER MESSAGE; the text arm and the copy are shared.** `internal/mailer/templates/` holds `chrome.html.tmpl` (the blocks that carry no meaning of their own — shell, brand, button, URL box, code block, footnote), one `<template>.html.tmpl` per message, a single `layout.txt.tmpl`, and `strings.{en,pt,es}.json`. `en` is the source of truth and a locale missing ONE message falls back to English per message, not per catalogue. Three shapes emerged from the content and nothing else: **action** (button plus the URL spelled out, so the host can be read before clicking and the message survives a client that will not render the button), **code** (the digits lead, BEFORE the prose — a sign-in code is read and discarded in ten seconds, and making the reader scan a paragraph first taxes them for the one thing they opened the message to get), and **notice** (no button slot at all). The eleven files DELEGATE to three shape templates rather than restating them: three were byte-identical before that, which is where "a layout per message" has stopped meaning anything, and accent/tint are data (indigo spans all three shapes). A message diverges by ceasing to delegate. **A layout must never `{{define}}` a shared block**: `text/template`'s Parse REPLACES silently and `ParseFS` walks the glob sorted, so a `chrome.button` pasted into any file sorting after `chrome.html.tmpl` rewrites it for all eleven — observed blast radius was a password reset with no anchor at all, suite green, and the credential exists in that message and nowhere else. `refuseDuplicateDefinitions` parses each file ALONE and refuses the boot naming both files; the parity check cannot see this, because `Lookup` still resolves. Shared blocks are namespaced `chrome.` for the mirror reason: unprefixed, a future message named `body` or `heading` would pass the parity check and render a bare `<tr>` fragment as the whole e-mail. `render` selects with `ExecuteTemplate(env.Template, doc)`, and `loadAssets` refuses to boot when a catalogue entry has no layout — on EITHER arm — the assets are embedded, so a mismatch is a binary that shipped wrong, not a runtime condition, and it must fail where a developer sees it rather than in a queued reset at three in the morning. The text arm lives in ONE FILE because plain text has no styling to differentiate — but it carries the same three shapes, because the slots are what carry the guarantee. The shape is therefore declared twice per message, and what makes that safe is the linkless test asserting both arms. **No file in that directory may begin with `_`**: the directory form of `//go:embed` silently drops such names, and `chrome` is composed by all eleven layouts, so the day someone simplified the directive every message would break at once. The eyebrow label and the shared sign-off are COPY, in the catalogues — the sign-off under the reserved key `_footer`, because repeating it across eleven entries in three locales would be thirty-three places to fix one typo. The outbox stores `(template, params)` rather than a rendered body — the row stays small, a copy fix reaches messages already queued, and a frozen body has already chosen its language. Copy is executed with `missingkey=error` so a param a constructor forgot fails the render instead of printing `<no value>` into a password-reset e-mail. **The text arm stays mandatory on every message** (`render` only emits `multipart/alternative` when both exist), and `session_revoked` / `reset_unavailable` carry no link — the first is anti-phishing (it reports killed sessions, the exact pretext a forgery uses, and a button there would train the reader to click one in the fake), the second is ADR-31 (a link would let mailbox control alone resurrect a password credential). **That is STRUCTURAL, on BOTH arms** — and the first attempt got it half right, which is worse than not trying: the HTML was split into shapes while the text arm stayed one layout with optional slots, so an injected `ActionURL` printed the raw URL — under a stray empty-label colon — two lines above the footnote promising the message carries no link. Every text client auto-linkifies a bare `https://`, and a multipart reader who refuses HTML sees only that arm. Gating each slot on its own field was tried and rejected too: it fixes whichever field you thought of, and that pass left `Code` leaking. **The SHAPE is the guarantee** — `chrome.shape_notice` and `text.shape_notice` have no button, no URL box and no code block, and a form with no slot cannot leak one it does not have. `TestLinklessMessagesCannotBeGivenALink` injects an `ActionURL` AND a `Code`, in all three locales, and asserts on both arms. Its positive counterpart is mandatory too: without `TestEveryLinkCarryingMessageRendersItsLinkInBothArms`, `NotContains(HTML, "<a ")` is satisfied by a tree where no message renders an anchor at all — and deleting the button from `password_reset` did exactly that, green. **Locale resolves recipient-first (mig 000035)**: `app_user.locale` — a profile field the user edits themselves, never an administrator — then the `Accept-Language` of whoever triggered the send, then `en`. NULL means *no preference*, not English. **The header is a weaker signal than it looks**, and the third rank exists to compensate: the browser speaks with two voices — the interface picks its language from `navigator.language` (and from the topbar picker, stored per device), while this fallback reads `Accept-Language`, a separate setting almost nobody configures. A Chrome showing a Portuguese foldex while sending `Accept-Language: en` is an ordinary configuration, and it mailed an English password-reset link to a user whose every screen was Portuguese. So the ANONYMOUS flows carry an explicit hint: `POST /api/auth/password/forgot` accepts a `locale` naming the language the SPA is DISPLAYING, and `localeForHinted` ranks it exactly where the header sits — below the recipient's stored preference, never above it. That ordering is the whole safety argument on an unauthenticated endpoint: naming a language can only choose the wording of a message the caller already caused to be sent, to an address they do not control, for an account that never stated a preference. Unrecognised values fall through to the header rather than being stored or echoed. Writing the displayed language INTO the account was tried and rejected — it cannot tell "never chose" from `""` (*follow my browser*), which Profile offers precisely so a preference can be undone, and an unprompted write on mount made that option inoperative. There is no CHECK constraint against a language list: the catalogues ship with the binary and a constraint only changes with a migration, so the two would diverge the first time a language was added — and the divergence would have the database refusing a language the binary can render. Validation lives in the handler, against the catalogues actually loaded. An invitation is the one message that cannot honour a preference, because the invitee has no account yet.

<a id="inv-036"></a>
### INV-036 — E-mail credentials live in URL fragments, never queries or access-log-visible paths.

Invite, reset and verification mail uses `/#invite=`, `/#reset=` and `/#verify=`. The SPA reads and removes the fragment at module scope before rendering; the initial HTTP request contains no token. Invite preview and OAuth invite start receive the token in a POST body, with `Optional` still enforcing CSRF when a session exists. Nginx logs `$request_method $uri $server_protocol`, never `$request`, query args or referrer.

<a id="inv-037"></a>
### INV-037 — A password reset proves the FIRST factor only.

*Guards:* `TestResetPassword_StillRequiresTheSecondFactor`

An account with a confirmed authenticator diverts into the same challenge login uses; otherwise a compromised mailbox would bypass 2FA outright. Locked by `TestResetPassword_StillRequiresTheSecondFactor`.

<a id="inv-038"></a>
### INV-038 — `AUTH_REQUIRE_2FA_FOR_ADMINS` diverts, it does not refuse, and has no privileged-session exception.

Password login, bootstrap and admin-invite acceptance without a confirmed authenticator get an `enroll_2fa` challenge instead of a session; OAuth uses the same decision. Promotion to admin revokes every existing session in the role-change transaction, forcing a fresh login/enrollment. `RequireAdmin` then re-checks the current confirmed factor, so enabling the policy also withholds `/api/admin` from legacy and refreshed sessions (403 `admin_2fa_required`) while `/api/auth/2fa/*` remains reachable; non-admins still get 404. This current-state gate plus promotion revocation is deliberate instead of a persisted session-assurance column. WHICH factor satisfies it is `instance_policy.admin_second_factor ∈ {any, totp_only}` (ADR-35 floors), `any` being the floor — an absent key on a policy document saved before ADR-37 decodes to `""` and takes it, because strict validation there would have refused EVERY policy write on an existing instance over a field the owner never touched. An admin can never remove their LAST acceptable factor; see mayRemoveFactor above.

<a id="inv-039"></a>
### INV-039 — Sessions are opaque tokens stored as sha256, and the CSRF header is checked against the SESSION ROW (ADR-30).

`session.{access,refresh,csrf}_token_hash` are `BYTEA` sha256 of 32 random bytes minted by `internal/pkg/secrets` — the raw value exists only in the cookie, so a `pg_dump` is not a session-hijack kit. sha256 rather than bcrypt is correct **here and only here**: the tokens are 256-bit random (nothing to grind) and resolution is on the hot path of every request; passwords keep bcrypt precisely because they are low-entropy. `X-Foldex-CSRF` is compared in constant time against `session.csrf_token_hash`, **never against the `fx_csrf` cookie** — naive double-submit is defeated by cookie injection from a sibling subdomain, where the attacker controls both sides of the comparison. `fx_csrf` is the one auth cookie deliberately NOT httpOnly (the SPA must echo it); its protection is that a cross-origin attacker cannot READ it.

<a id="inv-040"></a>
### INV-040 — Refresh rotation runs in ONE `SERIALIZABLE` transaction, and a replayed token kills the whole FAMILY.

*Guards:* `TestRefresh_GraceSiblingInheritsFamilyAndAbsoluteCeiling`

Consumed tokens land in `session_used_token` and are kept for the full retention window — that table IS the reuse detector's memory, so deleting on rotation would turn a replay into an ordinary 401. A hit inside the **10-second grace window** is a racing tab (React StrictMode's double mount, two tabs, a fast reload), and it issues a **SIBLING session in the same family, inheriting `family_id` AND `created_at`** — never new tokens on the existing row, which would invalidate whatever the winning request installed and sign a tab out at random. Inheriting `created_at` is what stops a client from riding the grace window to push the 90-day absolute ceiling forward forever. Outside the window it is a replay: revoke every session in the family, purge its used tokens, e-mail the owner. Locked by `TestRefresh_GraceSiblingInheritsFamilyAndAbsoluteCeiling` / `..._DiesWithTheFamilyOnReuse`.

<a id="inv-041"></a>
### INV-041 — Login is byte-identical for unknown e-mail, wrong password and disabled account.

Four mechanisms, all required: bcrypt **always runs** (against a dummy hash on a miss — skipping it is the classic ~80 ms oracle); one `401 invalid_credentials` body for all three; the per-e-mail rate bucket increments **for unknown addresses too** (not incrementing teaches the attacker which addresses are lockable, hence which exist); and a **250 ms duration floor**, not jitter — jitter only adds variance that averaging removes. `internal/pkg/attemptlimit`'s reserve-then-commit API (`Begin`/`Release`/`CommitFail`/`CommitSuccess`) is what makes the cap hold under concurrency: a plain check-then-act lets N parallel guesses all read the same pre-cap count while bcrypt runs.

<a id="inv-042"></a>
### INV-042 — `/api/auth/me` ALWAYS answers 200

, with `status ∈ {anonymous, setup_required, authenticated}`. A 401 there would recurse through the SPA's refresh interceptor on every cold boot. Mirrored on the client by a module-level single-flight `refreshPromise`: App mounts four authenticated queries at once, and without it all four would POST `/api/auth/refresh` with the same cookie and hit the reuse detector.

<a id="inv-043"></a>
### INV-043 — A non-admin gets 404 from `/api/admin/*`, not 403

— same reasoning as the row-level rule below: a 403 confirms the route exists and that the caller merely lacks the role.

<a id="inv-044"></a>
### INV-044 — No API call may leave the instance with zero active administrators.

An admin cannot demote, disable or delete themselves, and the last active admin cannot be removed by anyone. Zero admins is unrecoverable except by direct SQL. The guard counts `role IN ('owner','admin')` — with four roles, counting only `admin` would call an instance whose sole administrator is the owner "down to zero".

<a id="inv-045"></a>
### INV-045 — RBAC is four roles and a permission matrix, and content stays private per account (ADR-33).

`owner`/`admin`/`editor`/`viewer` (mig 000032, `user → editor`); what each may do lives in `internal/pkg/authctx/permissions.go`, never in a boolean. **Content permissions decide whether a write is ACCEPTED, never whose rows are visible** — §4's ownership rules are untouched, and a viewer sees exactly the same rows an editor does (their own). The matrix is a map lookup, so an unknown role resolves to the empty set and authorization fails CLOSED. `RequirePermission` answers **403**, not the 404 `RequireAdmin` gives: past that gate the caller already knows the surface exists, so an honest refusal leaks nothing. The write gate is mounted **per group and method-aware** on `/links`, `/notes`, `/tags`, `/import` so a mutating route added later is covered by construction; `/folders` and `/backup` gate per route instead, because both answer POST to operations that only READ (unlock proves a password to SEE contents; export serializes rows the caller already owns) and a blind method gate would lock viewers out of their own protected folders.

<a id="inv-046"></a>
### INV-046 — There is EXACTLY ONE owner, enforced by a partial unique index, and the seat moves only by transfer.

`app_user_single_owner_uniq` — not handler discipline, for the same reason cross-tenant references are FK-blocked. The owner's role and status are refused by every ordinary edit (`ErrOwnerImmutable`); `TransferOwnership` swaps both rows in a **single UPDATE** with a `CASE`, because the index is checked per statement: promote-then-demote fails on the promotion, and demote-then-promote leaves the instance ownerless mid-transaction. The outgoing owner is always the CALLER, never a path parameter — the only principal entitled to give the seat away is the one sitting in it. Both accounts lose every session. An invitation can never mint an owner (`invite_role_check`).

<a id="inv-047"></a>
### INV-047 — The audit trail outlives the accounts it describes (ADR-34).

`audit_log` uses `ON DELETE SET NULL`, never CASCADE, and denormalizes the e-mail beside the id: deleting an account must not erase the record of what it did, and after the row is gone the id alone identifies nobody. **There is no IP column** — `X-Forwarded-For` is only trustworthy behind a configured proxy, so an IP column would be authoritative-looking and attacker-controlled on a direct bind. `Audit` returns an error the caller LOGS, never propagates: the action already committed, and a 500 would invite a retry that performs it twice. Login success is recorded in `issueAndRespond` (the one choke point every credential path funnels through), never in `Login` — a password accepted that then diverts into a 2FA challenge is not a sign-in. Login failure writes ONE entry on the branch all three causes share, or the enumeration oracle comes back.

<a id="inv-048"></a>
### INV-048 — Instance policy has FLOORS that configuration cannot cross, and writing it is owner-only (ADR-35).

`internal/policy` is a leaf (`auth` imports it, never the reverse) persisting one JSON document in `app_setting` — which is outside the backup surface in BOTH directions, so a crafted zip cannot rewrite the password floor. Each floor is the value the code used before it was configurable, so a never-configured instance behaves exactly as before and a configured one can never be weaker. `validatePassword` is a **METHOD** so the compiler drags every call site through the configured floor; as a package function it would silently keep applying the constant. Admins READ the policy, only the owner WRITES it: an admin who could lower the floor could then walk in through it.

<a id="inv-049"></a>
### INV-049 — `google_auto_provision` revokes ADR-31's invite-only rule, and every safeguard is load-bearing.

OFF by default; refused without a non-empty domain allowlist (open list + provisioning = any Google account becomes a tenant); the default role is refused at THREE layers and can never be administrative; every refusal is the SAME `not_linked` an unknown address always gave (a distinct answer would tell an anonymous caller which instances are open, or enumerate the allowlist one guess at a time); the allowlist gates only the paths that CREATE access (conversion, provisioning) and NOT an already-linked login, or an owner could lock themselves out with a list excluding their own domain; domains match EXACTLY, never by suffix. The provisioned row and its identity are inserted in ONE transaction — mig 000021's deferred trigger requires an active account to hold a credential, and this one never holds a password.

<a id="inv-050"></a>
### INV-050 — A row belonging to another user is reported 404, never 403.

*Guards:* `TestCrossUser_GetOfAnotherUsersRowIsNotFound`

A 403 confirms the id exists, which turns a dense `BIGSERIAL` space into an enumeration oracle over other tenants' content. Locked by `TestCrossUser_GetOfAnotherUsersRowIsNotFound`.

<a id="inv-051"></a>
### INV-051 — Cross-tenant references are blocked by the DATABASE, not by handler discipline.

`folder` carries `UNIQUE (user_id, id)`, and `link.folder_id` / `note.folder_id` / `folder.parent_id` are **composite FKs** on `(user_id, folder_id)` with `ON DELETE SET NULL (folder_id)` — the column list is mandatory, or deleting a folder would try to null the `NOT NULL` `user_id`. The one hole this cannot cover is `link_tag` (it lost its FK in mig 000014 and `tag_id` has no `user_id` to compose with), so `tags.SetEntityTags` validates tag ownership itself, in the same tx, before writing. Both are locked by `internal/security`.

<a id="inv-052"></a>
### INV-052 — `tag.name` is unique PER USER

— `UNIQUE (user_id, name)` since mig 000017 (DB + `tag_name_taken` 409 on conflict). Two users may each own a tag called `work`.

<a id="inv-053"></a>
### INV-053 — `tag.color` / `folder.color` are CSS strings

validated by `internal/pkg/cssvalid` — only hex (`#abc`/`#abcd`/`#aabbcc`/`#aabbccdd`) or `linear-gradient(135deg, #hex, #hex)`. Frontend `web/src/lib/tagColor.ts` is the SINGLE parser (use `primaryColor(c)` for `color:`/`color-mix(…)` since those don't accept gradients). Without `cssvalid`, `red url("https://evil/exfil")` turns every chip render into a tracking pixel.

<a id="inv-054"></a>
### INV-054 — `link.url` is unique PER USER

— `UNIQUE (user_id, url)` since mig 000017 (was global in mig 000002). UNIQUE violations are **409 `url_taken`, never 500** — `internal/links/repository.go` uses `errors.As(*pgconn.PgError)` + `ConstraintName` match on `link_user_url_unique` (string-match on wrapped messages would silently break behind any layer that drops `Unwrap`). Two users may save the same URL independently.

<a id="inv-055"></a>
### INV-055 — `link.slug` is NOT NULL and GLOBALLY UNIQUE

— deliberately NOT per-user, unlike `url`/`tag.name`, because `/go/{slug}` resolves with **no session** and therefore has no tenant to disambiguate by; a cross-tenant collision falls back to the `-2`/`-3` suffix. Same for `note.slug` and `/n/{slug}`. Lowercase + hyphenated (mig 000009) with CHECK `^[a-z0-9]+(-[a-z0-9]+)*$ AND NOT all-numeric` so `/go/42` always resolves to id 42. Auto-derived from `title` via `Slugify`; user can override in `LinkDialog`. Resolution in `redirect.handler`: int parse → ID lookup → slug lookup → 404. Backup/import/export propagate slug end-to-end; backup/import pre-load relevant global collisions and allocate `-2`/`-3` in memory, never query once per candidate.

<a id="inv-056"></a>
### INV-056 — `click_log` is the single source of truth for clicks.

`link.click_count`/`last_clicked_at` columns no longer exist (mig 000006); both are derived either via `LEFT JOIN LATERAL` for a row projection or a set-based aggregate restricted by owner, `entity_kind`, and the candidate IDs for mixed/page queries. `/go/{id-or-slug}` is the **only** path that INSERTs into `click_log`, inside a tx that also verifies the link exists (404 otherwise) — never an UPDATE on `link`.

<a id="inv-057"></a>
### INV-057 — `link_tag` is the only place link↔tag lives.

No denormalization. M:N is mutated only through `links` handlers (Create/Update with `tag_ids`). Tag deletion cascades to `link_tag` (FK `ON DELETE CASCADE`); links survive.

<a id="inv-058"></a>
### INV-058 — `link_tag` and `click_log` are polymorphic (mig 000014) — shared by `link` and `note` via an `entity_kind ∈ {'link','note'}` discriminator on `entity_id`.

*Guards:* `TestCrossContamination_LinkAndNoteRowsDoNotLeak`

Every query against either table **MUST filter `entity_kind`**: link ids and note ids occupy the same numeric space, so an unscoped join can silently attach a note's tag/click to an unrelated link (or vice versa) that happens to share the same id — `TestCrossContamination_LinkAndNoteRowsDoNotLeak` (`internal/notes`) locks this. The FK from these tables to `link(id)` was dropped when they were polymorphized (a polymorphic column can't reference two tables), so cascade-on-delete is **app-level**: any code path that deletes a `link` or `note` row (`links.Repository.Delete`, `notes.Repository.Delete`, `folders.Repository.DeleteCascade`, the importer's wipe-mode re-import) must purge its own `link_tag`/`click_log` rows inside the same tx first — never rely on `ON DELETE CASCADE` for either table again. Detail: ADR-27.

<a id="inv-059"></a>
### INV-059 — `note.body_html` is sanitized server-side on every write, no exceptions.

`internal/pkg/htmlsanitize.Sanitize` (bluemonday, explicit allowlist matching the Tiptap editor's output — StarterKit + Image/Link + the toolbar's TextAlign/Color/FontFamily; allows `<span>` and the `text-align`/`color`/`font-family` inline styles with **regexp-pinned values** so no `url()`/`expression()` can ride in; still no `<table>`, no `data:` URLs) runs in `notes.CreateInput/UpdateInput.Normalize()` **and** defensively again in `notes.Repository.Create/Update` (idempotent, cheap) **and** in every backup restore mode (`backup/db.go`'s `sanitizeNoteBody`) — restore writes go straight to SQL, bypassing the DTO layer entirely, so skipping it there would let a crafted backup zip plant a script payload that renders raw on the public, unauthenticated `GET /n/{id-or-slug}` route. `body_text` (the ILIKE/trigram search column) is always derived server-side from the sanitized HTML, never accepted from the client.

<a id="inv-060"></a>
### INV-060 — `internal/entries` (`GET /api/entries`) is the single, read-only source for the interleaved link+note grid.

A `UNION ALL` over `link`+`note`, wrapped in a derived table so `ORDER BY` can use expressions like `lower(title)` (Postgres forbids that directly under a set operation). Mutations never go through this package — they stay on `/api/links`/`/api/notes`. Adding a filter to one arm's `List` (links or notes) without mirroring it in `entries.Repository.List` silently breaks grid/search parity for that filter.

<a id="inv-061"></a>
### INV-061 — `preview_status ∈ {pending, ok, failed}`.

Preview worker is the only background writer (`internal/preview`); manual upload is the deliberate synchronous override. **Manual upload short-circuits and supersedes** the worker: `ReplaceOGImage` sets `og_image_url`, `preview_status='ok'`, `preview_error=NULL` atomically and returns the exact superseded URL for post-commit cleanup; worker's `process()` checks at the top and skips both HTML fetch and screenshot fallback if `og_image_url` is non-empty. Migration `000030` adds monotonic `preview_generation`: every refresh increments it, and every worker metadata/status/image publication must match the claimed generation plus the pending/image guards. `updated_at` remains part of the CAS but is insufficient by itself when two refreshes share a timestamp. A CAS loss deletes the newly uploaded fallback object instead of orphaning it.

<a id="inv-062"></a>
### INV-062 — Change-check claims carry their configuration snapshot.

`SystemFindDueForCheck` atomically claims each due, unlocked link and returns only the worker projection (`id`, owner, URL, title, interval, fingerprint, claimed `last_checked_at`); the worker never hydrates the full link/click aggregate. Result writes CAS on that exact claim token and re-check the locked-folder predicate, so a move into a protected folder between fetch and publication suppresses state and push. Push is queued only after a successful CAS, so opt-out, URL/interval changes or a newer claim during fetch cannot publish stale state. Actual URL/interval changes clear the old baseline and make the link immediately due; unchanged fields in a full edit payload preserve state. Push delivery uses fixed lifecycle-owned workers, a 32-message queue, and a sender-wide four-delivery semaphore shared by background and test fan-outs; queue overflow drops the newest notification, and `Stop` cancels in-flight sends. Preview and change-check fetch pools are independently capped at 8 workers each, including constructor-level defense.

<a id="inv-063"></a>
### INV-063 — Folders are 1:N exclusive AND nestable.

A link belongs to at most one folder via `link.folder_id`. Folders nest via `folder.parent_id` (self-FK). Both FKs `ON DELETE SET NULL` — deleting a folder promotes children to root. `?cascade=1` recurses via CTE through the whole subtree (existing `ON DELETE CASCADE` on `link_tag`/`click_log` cleans up). `folder.name` is NOT unique. Detail: ADR-19.

<a id="inv-064"></a>
### INV-064 — A folder cascade never crosses an unproved password boundary.

`DeleteCascade` authorizes the root with its current unlock token, materializes an owner-scoped subtree and locks every row in the transaction before mutation. If any descendant other than the root has `password_hash`, it refuses the whole operation with `409 descendant_protected` plus `count`; an unlock for the root never authorizes an independently protected child. API tokens remain rejected. The frontend retries a `folder_locked` delete only after the existing password prompt mints a session-only token, and surfaces both `folder_locked` and `descendant_protected` inline.

<a id="inv-065"></a>
### INV-065 — Folder password protection is per-folder, backend-enforced, and split into two separate mechanisms — never conflate them.

`folder.password_hash` (nullable, bcrypt via `internal/folders/password.go`) does NOT cascade to subfolders — a subfolder inside a locked folder needs its own password to be independently protected. (1) **Redaction is always-on, no token check**: `folders.Repository.List`/`Get` unconditionally zero `preview_links`/`preview_folders` whenever `has_password=true`, in every response, regardless of any unlock token — this is what makes `FolderRapidView`'s hover popover safe with zero frontend leak-prevention logic. (2) **Content-gating requires a valid unlock token** for the TWO operations that reveal real contents: `GET /api/entries?folder_id=X` and `GET /api/folders?parent_id=X`, both `403 folder_locked` without a valid `X-Foldex-Folder-Unlock` header. The unlock endpoint is **rate-limited per folder** (`internal/folders/ratelimit.go`, in-memory): 5 wrong passwords in a row → `429 too_many_attempts` + 1-hour lockout (`Retry-After` header; a correct password resets the counter; restart clears the state — acceptable for single-user/local, bcrypt cost is the real floor); `401 wrong_password` carries `failed_attempts`/`attempts_remaining`. The frontend reveals `password_hint` only **after the 3rd failed attempt** (non-secret, but held back to nudge recall first). `POST /api/folders/{id}/unlock` verifies the password (bcrypt) and issues a token whose HMAC input includes the folder's CURRENT `password_hash` — changing or removing the password auto-invalidates every previously issued token, no revocation list needed. TTL 24h is a safety ceiling only; the frontend never persists the token past a page reload (unlock is session-only, by design — no `localStorage`). **Changing or removing an EXISTING password requires the current password** (`UpdateInput.CurrentPassword`, checked inside the same SERIALIZABLE tx as the parent-cycle check); setting a password for the first time needs no proof. Drag-and-drop/folder-picker moves INTO a locked folder are never gated (write-only, doesn't reveal contents); `exporter`/`importer`/backup are not gated either (already-trusted owner operations), but `password_hash` round-trips through backup **verbatim** in all 3 restore modes (`backup.FolderRow.PasswordHash`) — never re-hashed, never dropped. Detail: ADR-28.

<a id="inv-066"></a>
### INV-066 — Master password is RECOVERY ONLY, never a view bypass (ADR-29, supersedes ADR-28's "no admin bypass" clause for recovery).

The master password (bcrypt in the `app_setting` KV table under key `master_password_hash`, hashed via `internal/pkg/pwhash` — the single shared hash helper for folder + master passwords) is set/changed/removed from the Settings UI via `/api/settings/master-password` (`internal/settings`; `GET` returns only `{configured, hint}`, NEVER the hash; min length 8). An optional NON-secret reminder (`app_setting` key `master_password_hint`, same rules as `folder.password_hint`: returned verbatim, never hashed, never equal to the password) is TRI-STATE in `SetMasterPassword` (same tx as the hash): nil = keep the existing hint (changing the password with an empty reminder field must NOT wipe it), "" = clear, value = set; `ClearMasterPassword` drops hash + hint together. The Settings form clears every field after save and shows the stored hint read-only. The Settings UI adds a client-side **strength meter** + **confirm-password** field (guidance only — the backend enforces just the min length). Its ONLY power over folders is `POST /api/folders/{id}/reset-password`, which verifies the master and CLEARS that folder's `password_hash` + `password_hint` (`ResetPasswordByMaster`) so a new password can be set via the normal first-time-set flow — it does NOT unlock the folder for viewing and does NOT mint an unlock token. `400 master_not_configured` / `401 wrong_master_password`. The `folders` handler consumes a narrow `MasterPasswordVerifier` interface (defined in `folders`, satisfied by `*settings.Repository`) so `folders` never imports `settings` (no cycle). Recovering a forgotten MASTER is still a direct DB edit — it's the root secret.

<a id="inv-067"></a>
### INV-067 — `folder.password_hint` is NON-secret and MUST NEVER equal the password (ADR-29).

Unlike `password_hash`, the hint is returned verbatim in every folder response (`Folder.PasswordHint`, not redacted) and shown on the unlock prompt — surfacing it is the point. The "hint ≠ password" invariant is enforced at create (plaintext compare) AND update (`bcrypt.CompareHashAndPassword(effectiveHash, hint)` inside the SERIALIZABLE tx — catches equality without the plaintext). Removing the password auto-clears the hint (dead data otherwise). `app_setting` + `password_hint` round-trip through backup **verbatim** in all 3 modes (`backup.AppSettingRow`, `backup.FolderRow.PasswordHint`) — the master hash is never re-hashed, and wipe restores exactly the snapshot's settings (including "no master" for a pre-ADR-29 backup). Detail: ADR-29.

<a id="inv-068"></a>
### INV-068 — Home view excludes links inside folders.

`GET /api/links?ungrouped=1` returns `folder_id IS NULL` only. A link never appears in two places. The SPA grid reads `['entries']`: a folder_id PATCH must **remove** the card from those caches (`removeCachedEntry`) rather than mapping `folder_id` in place, or the home view keeps rendering a row the backend would no longer return.

<a id="inv-069"></a>
### INV-069 — Tag filter and folder scope compose via AND.

Inside a folder, selecting a tag narrows that folder's content (`folder_id = X AND tag_id IN (...)`). Sidebar stays interactive — backend already supports the composition.

<a id="inv-070"></a>
### INV-070 — Internal IDs never appear in the URL.

Folder navigation lives in component state only — no `?folder=N`, no `/folder/:id`. Same for tooltips: `/go/{id-or-slug}` is implementation detail, UI label is just "Acessar". The slug IS exposed in `LinkDialog` (the user owns it as the share-friendly path).

<a id="inv-071"></a>
### INV-071 — Folders come BEFORE links in the grid except in alpha sort.

Default (Novos / Top / Recentes) renders `folders.map(...)` first, then `links.map(...)`. `alpha`/`alpha_desc` interleave by name so alphabetical order is honest — **within two blocks, pinned entries and then everything else**, because "pinned first" is not one of the rules alpha is allowed to break. `mergeAlphaCells` re-derives that split instead of trusting the incoming order: folders arrive from a different query, so the entries' server-side `pinned DESC` cannot survive being merged with them. It once did trust it, and a pinned "Zebra" rendered below an unpinned "Apple" in the one mode where nothing tested it.

<a id="inv-072"></a>
### INV-072 — viewMode + foldersCompact are per-context.

Persisted under `foldex.viewMode.map` and `foldex.foldersCompact.map` keyed by `home` or `folder.<id>`. Default `cards` / `false`. Home `useEffect` prunes orphan `folder.<id>` keys from BOTH maps on the same pass — never let them drift.

<a id="inv-073"></a>
### INV-073 — FolderCard `compact` mode + RapidView popover.

Compact hides the 2×2 preview and shows a thin strip. Hover/focus on the title mounts `FolderRapidView` (portal) listing `preview_folders` then `preview_links` from the existing `useFolders` payload — **no extra API call**. Cap 10 items + `+N more` footer (`link_count + folder_count − rows.length`). Empty folder = no popover. Show delay 220 ms.

<a id="inv-074"></a>
### INV-074 — Drag-and-drop wiring.

`LinkCard` is the only `draggable`; payload `application/x-foldex-link` carries the link id. `FolderCard` accepts → `onDropLink(linkId, folderId)`. `LinkCard` accepts → `onMergeWith(sourceId, targetId)`. Mutations live in `App.tsx`; cards stay UI-only. Same-card drops are no-ops.

<a id="inv-075"></a>
### INV-075 — Imports are idempotent by URL.

Re-importing the same `bookmarks.html` produces `skipped` matches. When JSON export carries `click_count`, importer materializes that many `click_log` rows stamped at the link's `created_at` — only on fresh insert. `click_count` is capped at 10,000 per link AND 1,000,000 cumulatively per request; the per-link cap alone would permit a 50,000×10,000 amplification.

<a id="inv-076"></a>
### INV-076 — Image input has a 50 MP decode cap.

`imageopt.Optimize` calls `image.DecodeConfig` before `Decode` and refuses with `ErrTooLarge` if `width × height > 50_000_000`. Without this, a ~30 KB PNG declaring 60000×60000 allocates ~14 GB. Upload entry point also caps body at 5 MiB — both caps must stay.

<a id="inv-077"></a>
### INV-077 — Uploads and screenshots are always re-encoded via `internal/imageopt`

(decode → downscale Catmull-Rom ≤1024 px → composite over white → JPEG q82). Exception: source already JPEG AND no resize AND re-encode came out larger → keep original (no-regression). PNG/GIF/WebP **always** re-encoded. Manual uploads and captured screenshots use operation-owned `{prefix}/{id}.{uuid}.{ext}` keys so failed or superseded work can delete only its own bytes. Animated GIFs collapse to first frame.

<a id="inv-078"></a>
### INV-078 — Old image objects are purged only after database publication succeeds.

New uploads and captures first write an operation-owned key. Manual upload atomically swaps the URL and returns the exact superseded value; captures CAS their exact URL into `link`. Only then may the caller remove deterministic legacy variants and the previously referenced local object. Publish failure, capture CAS loss, or a concurrent delete removes only the new operation-owned key. Pre-deploy files in RustFS are NOT backfilled and remain servable. `Uploader` requires `DeleteObject` for this cleanup.

<a id="inv-079"></a>
### INV-079 — Cloud metadata ranges and RFC6598 shared address space (`100.64.0.0/10`) are always blocked

by the preview fetcher (no env opt-out). This includes EC2 IMDS, ECS credentials, EKS Pod Identity, Alibaba and Tencent metadata on their IPv4/IPv6 endpoints. `PREVIEW_STRICT_SSRF=1` *additionally* blocks the complete IANA special-purpose registries, including loopback, RFC1918, benchmarking/documentation/reserved space, link-local and IPv6 ULA. Default remains permissive for ordinary intranet addresses because intranet is foldex's primary use case (ADR-12). The shared policy lives in `internal/pkg/netpolicy`.

<a id="inv-080"></a>
### INV-080 — SSRF dialer is checked twice.

`preview.safeDialer.DialContext` runs `LookupIP` + IMDS/private guard pre-dial AND `conn.RemoteAddr().(*net.TCPAddr)` post-dial. The pre-dial leg is fast-fail; the post-dial leg defeats DNS rebinding. Post-dial type-assert is fail-closed.

<a id="inv-081"></a>
### INV-081 — A stored image that turns out to be GONE re-arms its own preview, and the sentinel that gates it is the whole safety of the feature.

`maybeScreenshot` fires only for a link whose `og_image_url` is EMPTY, so a row pointing at bytes the object store no longer has is stuck: the card is blank forever while `preview_status` still reads `ok`. The file proxy therefore clears the reference and sets `pending` — `pending`, not `failed`, because nothing failed; the bytes are simply gone and pending is the state the worker picks up. **It takes that branch only on `ports.ErrObjectNotFound`**, a sentinel the storage adapter raises from minio's `NoSuchKey` CODE (never its message — a timeout returns the same shape with different text). Any error would mean one unreachable moment clears every `og_image_url` on the instance and re-screenshots the whole library; this is the same rule as push subscriptions above, where 404/410 removes the row and a transport error never does. **The UPDATE is conditional on `og_image_url` still equalling the URL that 404'd** — a screenful of broken cards is a screenful of concurrent 404s, and without the predicate that is thirty-three UPDATEs and thirty-three captures of a handful of links; it also protects a manual upload that landed between the browser's request and this write, which no longer matches and so is not silently discarded. Only the caller whose UPDATE actually changed a row enqueues. `notes/` keys are NEVER healed: note media is user-uploaded and nothing can regenerate it, so clearing the reference would destroy the only record the image ever existed. Every failure in this path is logged and swallowed — it rides a READ that has already decided its answer, and turning self-healing into a 500 would make a broken thumbnail break the page.

<a id="inv-082"></a>
### INV-082 — A card whose image fails to load falls back to its glyph, never to the browser's broken-image icon.

`LinkCard` had this; `NoteCard` and the note reader did not, so a cover whose object was gone painted a broken-image icon in a 150px slot — which reads as a corrupt note rather than a missing file. The errored flag RESETS on the image URL, or one broken cover would suppress every later one on that card.

<a id="inv-083"></a>
### INV-083 — Screenshot is a FALLBACK, never default.

`preview.Worker.maybeScreenshot` runs only when **all** of: (a) HTML fetch returned empty `og:image`, (b) link still has no `og_image_url`, (c) `preview.IsPublicURL(url)` is true, (d) worker was wired with `WithScreenshotFallback(sc, up)`. Decode-bomb errors abort the fallback (don't write raw PNG to RustFS). Detail: ADR-16.

<a id="inv-084"></a>
### INV-084 — Every screenshot capture gets a fresh Chromium BrowserContext and a per-capture strict local proxy.

The Chromium process stays pooled, but cookies/cache/storage never cross captures; disposal uses a fresh bounded context so request cancellation cannot preserve profile state. Every HTTP navigation, redirect, subresource and CONNECT goes through the per-capture proxy; each proxy caps active CONNECT tunnels at 32. A second strict proxy is pinned at process launch because Chromium's implicit localhost bypass is decided by the process proxy config; it catches any request that escapes the context proxy. Both block the complete IANA special-purpose registries and validate the connected TCP peer after dial, independent of `PREVIEW_STRICT_SSRF`; any blocked request invalidates the whole screenshot. **Tunnel registration is INTERLOCKED with teardown, not merely ordered after it.** A CONNECT holds its semaphore slot from the moment it is admitted but joins the tracked map only after the 200 is flushed, and the client can open the next tunnel in that gap — so `closeTunnels` walks a map the tunnel is not in yet, it survives the teardown, its copy goroutines block on a peer nobody closed, and the slot is never returned. `trackTunnel` therefore refuses and closes when `blocked` is already set, under the same mutex `closeTunnels` holds, exactly as it already did for `closed`. Missing that half meant a tunnel could keep relaying bytes after the capture was invalidated — and it surfaced only as a liveness timeout on a loaded CI runner, on an unrelated pull request. Captures are serialized because the launch proxy's blocked signal belongs to the process generation and cannot safely attribute concurrent escapes. Chromium runs as the non-root container user with its sandbox enabled and direct UDP egress disabled (`force-webrtc-ip-handling-policy=disable_non_proxied_udp`, `disable-webrtc-multiple-routes`, `disable-quic`). One explicit `screenshot.Pool` instance is shared by the preview worker and manual endpoint; lifecycle state and test seams never live in package globals. Queue wait (5 s), browser startup/connect and page capture have independent bounded budgets under a 70 s caller envelope. Startup never holds the pool mutex; retirement detaches tracked teardown from capture latency, while shutdown rejects new work, cancels startup and active captures, waits up to 7 s, then force-kills and cleans unfinished generations before returning inside the process-wide 12 s deadline. The pool retains launcher + PID; every launch failure cleans the profile, while failed connect/context cleanup/close hard-stops the process with `Kill` + context-aware bounded `Cleanup` instead of leaking a 150 MB+ browser. Profile cleanup uses rooted, symlink-safe traversal in bounded batches and checks cancellation between batches.

<a id="inv-085"></a>
### INV-085 — Manual screenshot endpoint applies the same SSRF gate.

`internal/links/screenshot_handler.go:CaptureAndStore` takes a `links.URLPolicy` (wired to `preview.IsPublicURL`). Rejects non-http(s) → 400 `invalid_scheme`; special-purpose/metadata targets → 400 `private_target`. **Nil policy is a config error, not SSRF rejection**: 500 `policy_unconfigured`. Router additionally panics at boot if `Screenshotter != nil && ScreenshotURL == nil` — surface wiring errors at deploy, not per-request.

<a id="inv-086"></a>
### INV-086 — Screenshot resource budgets are fail-closed.

Each strict proxy limits active connections/CONNECT tunnels to 32 and aggregate transferred bytes to 32 MiB; CDP additionally limits each capture to 256 browser requests, including multiplexed HTTPS traffic inside CONNECT. Exceeding any budget invalidates the capture. DNS policy gets 5 s and post-capture storage gets a separate 10 s context. Manual capture admits at most two users globally and one request per user. Preview enqueue deduplicates queued IDs and coalesces explicit refreshes arriving during a running job into one rerun; periodic recovery never creates reruns for scheduled work. HTTP, workers, and Chromium shut down concurrently under one 12 s process deadline; the pool uses 7 s of grace before force-killing and cleaning unfinished generations. Launch failure kills Chromium and retries profile cleanup. Raw fetch/Chromium errors are never persisted or included in logs.

<a id="inv-087"></a>
### INV-087 — `POST /api/links/url-metadata` reuses the preview `Fetcher` — same SSRF posture, no duplicate HTTP client.

`internal/links/metadata_handler.go` injects `links.MetadataFetcher` (adapter over `*preview.Fetcher` constructed in `main.go`). The route is POST so CSRF, `writeGate` (`PermContentWrite`) and the mutating API quota all apply — a viewer or a token without write is 403 and never dials. Body `{url}` (query `url` still accepted). Endpoint rejects non-http(s) → 400 `invalid_scheme`, URL > 2 KiB → 400 `invalid_url`. An in-flight slot (2 global, 1 per user, same shape as screenshot `acquireCapture`) refuses extras with 429 `metadata_busy` **before** FetchMetadata/dial, so a flood cannot starve the singleton Chromium pool. Resolution is HTTP/oEmbed first (same SSRF-guarded client as the preview worker; 5 s cap), then Chromium via the screenshot pool (10 s) when the origin answers 401/403/429, the HTTP pass times out, or the title is empty/interstitial — screenshot stays a fallback (INV-083), every capture still gets a fresh BrowserContext + strict proxy (INV-084), and Chromium is gated by `IsPublicURL` like the manual screenshot endpoint (INV-085). DNS/TLS/5xx and SSRF refusals never launch Chromium. If both passes miss, the handler answers **200 with empty fields** (not 502): a third-party bot wall is not an API fault, and the dialog can still Save with the hostname. Internal error text never leaves the process. Returned fields are truncated via UTF-8-aware `truncateRunes`: **title at `links.MaxTitleBytes`** (single source of truth — same constant the Create/Update DTOs enforce, so a pre-filled title always passes Save), description at 4 KiB, favicon/og_image URLs at 2 KiB.

<a id="inv-088"></a>
### INV-088 — oEmbed enrichment reuses the SAME `preview.Fetcher` client — never a second HTTP stack.

`internal/preview/oembed.go:fetchOEmbed`. The discovery `OEmbedURL` (from `<link rel="alternate" type="application/json+oembed">`) is attacker-controlled HTML, so `fetchOEmbed` enforces `scheme ∈ {http, https}` BEFORE building the request (Go's default transport would read `file:///…` since the IP-level dialer never fires for non-http(s) schemes). Merge contract: HTML always wins; oEmbed only fills empty fields. Detail: ADR-25.

<a id="inv-089"></a>
### INV-089 — JSON request bodies are capped at 64 KiB.

Every POST/PATCH handler in `links`/`folders`/`tags` wraps `r.Body` with `http.MaxBytesReader` before `Decode`. Realistic payloads are well under 4 KiB; surface is hostile.

<a id="inv-090"></a>
### INV-090 — Stats handler clamps every numeric knob via `clampInt`.

`?days` ∈ [1,365], `?limit` ∈ [1,100]. Without the cap, `?days=2147483647` lands in a `generate_series(...)` and the planner attempts it.

<a id="inv-091"></a>
### INV-091 — The cookie `Secure` flag is derived from `AUTH_PUBLIC_URL`'s scheme, NOT from the bind address.

*Guards:* `TestLoad_CookieSecureFollowsThePublicURLScheme`

Getting it wrong fails silently — a browser drops a `Secure` cookie over plain HTTP without a word, so login "succeeds" and the next request is anonymous — so it is never read from the environment directly. What decides it is the scheme of the origin **the browser** talks to; the bind answers a different question ("is the backend reachable from the network?"), and the two disagree in exactly the topology this project recommends: the binary on `127.0.0.1` with nginx terminating TLS in front. Deriving from the bind there concludes "dev, plain HTTP" and ships session cookies with no `Secure` flag to a browser on HTTPS. The read is from the **environment**, not from `c.AuthPublicURL`, because that field defaults to `http://localhost:9088` — a guess about where links point, not a statement that the browser is on plain HTTP, and trusting it would turn `Secure` off for every deployment that binds `0.0.0.0` without configuring a public URL. No explicit `AUTH_PUBLIC_URL` falls back to the bind heuristic. Locked by `TestLoad_CookieSecureFollowsThePublicURLScheme`.

<a id="inv-092"></a>
### INV-092 — `SetSession` expires `fx_pa`.

A live session and a half-finished login are mutually exclusive, so establishing one ends the other in the single function that defines "signed in on the wire" rather than at each of the four call sites. Only the 2FA path cleared it before; a challenge abandoned mid-flight left its cookie beside a fresh session for the rest of its TTL.

<a id="inv-093"></a>
### INV-093 — Boot refuses the insecure-by-default combo.

`config.validateSecureDefaults` errors if `BACKEND_BIND` is non-loopback AND `AUTH_ENABLED=0` — that combination attributes every request on a reachable port to the bootstrap administrator with no credential at all. `AUTH_ENABLED=1` satisfies the guard on its own, which is what lets the shipped compose stack (nginx on `0.0.0.0`) boot. `CORS_ORIGINS` is NOT part of this check — a wildcard origin is a different failure and is not gated here.

<a id="inv-094"></a>
### INV-094 — nginx ships defense-in-depth headers

*Guards:* `scripts/test-nginx-headers.sh`

(all with `always` for 4xx/5xx): HSTS, X-Frame-Options DENY, X-Content-Type-Options nosniff, Referrer-Policy no-referrer, Permissions-Policy, strict CSP. CSP allows `'unsafe-inline'` ONLY for style-src (emotion runtime); script-src stays strict. **NO `location` block in `web/nginx.conf` may declare an `add_header`**, and that rule is the whole reason `Cache-Control` is a `map` (`$fx_cache_control`, in `nginx.main.conf`) rather than a directive where it applies. nginx inherits `add_header` from the enclosing level only while the inner level declares NONE of its own — one header in a location silently discards all six above it. Three locations did exactly that (`= /index.html`, `= /sw.js`, `= /registerSW.js`), and because `location /` reaches index.html through `try_files`' internal redirect, the SPA's own document shipped with **0 of 6** while `/assets/*.js` and the webmanifest carried 6 of 6 — backwards, since CSP, X-Frame-Options and `frame-ancestors` govern the DOCUMENT and do almost nothing on a script file. The map keys on `$uri` AFTER the internal redirect, and an empty value emits no header at all, so revisioned assets keep default caching. `scripts/test-nginx-headers.sh` boots the real config and MAKES THE REQUESTS — reading the file cannot answer this, and no linter flags it. It asserts VALUES, not names: a first version checked names only, on seven responses that were all 200 from static locations, and three mutations survived it — stripping ` always` (every 4xx/5xx drops to 0/6), neutering every value (`default-src *`, `ALLOWALL`, `max-age=0`: present and useless), and re-adding an `add_header` to `location /api/` with two-space indentation, because the source guard checked indentation rather than NESTING. It now probes the proxied locations (they answer 502 with nothing listening, which is the only way `always` is under test at all), carries a negative check that `script-src` contains no `unsafe-`, walks brace depth in awk, and asserts that BOTH redirects ignore a forged Host header — the `:8080` HTTP→HTTPS server and the `:8443` `error_page 497 =301` (plain HTTP spoken to the TLS port redirects to the baked host instead of dead-ending on nginx's default 400). **The CSP allows `https://fonts.googleapis.com` in style-src, `https://fonts.gstatic.com` in font-src and `blob:` in img-src** — `foldex.css` opens with an `@import` of the brand faces and the link dialog previews a chosen file through `URL.createObjectURL`. Both were invisible until the policy started applying at all; self-hosting the fonts would retire the first two and is a follow-up, not an oversight.

<a id="inv-095"></a>
### INV-095 — All CI actions are SHA-pinned, not tag-pinned.

Each `uses:` line carries a 40-char commit SHA + `# vX.Y.Z` comment for Dependabot. Major tags are mutable; a compromised upstream could swap silently. `govulncheck` + `bun audit` run as informational steps (`continue-on-error: true`) — surface CVEs without gating mid-flight releases.

<a id="inv-096"></a>
### INV-096 — CI uses the GitHub runner's host Docker daemon, not a `docker:dind` service.

Any future workflow service image must carry both a version and an immutable `sha256` digest. The backend job resolves the runner's executable `google-chrome`, fails if it is absent, and exports `CHROME_PATH` before the single coverage-instrumented suite so live-browser security tests cannot silently skip. Extension Bun tests are part of the blocking frontend job.

<a id="inv-097"></a>
### INV-097 — A tag push can never publish.

`release.yml` is `workflow_dispatch`-only and its credential-free first job refuses any workflow ref except `refs/heads/main`. The required target is strict `vMAJOR.MINOR.PATCH` (matching both version files at the main tip) or a full 40-character SHA; the resolved commit and workflow SHA must be ancestors of freshly fetched `origin/main`. Semver tags must not preexist and are created only after both protected-environment publishers complete, so a failed build cannot leave an official release tag. Publishers check out the validated SHA and obtain Docker Hub secrets only through `release`. Historical SHA targets never move `latest` backward — `flavor.latest=false` so the explicit `type=raw,value=latest` gated on `publish_latest` is the only source; `latest=auto` would mint `:latest` on every semver cell even when the target is no longer the tip. The backup-agent cell publishes into the same Docker Hub repository as backend, with every tag suffixed (`2.18.0-backup-agent`, `sha-<short>-backup-agent`, `latest-backup-agent`); inspect looks up that suffixed SHA tag, never the unsuffixed backend one. Inspecting the sibling tag after a successful `imagetools create` fails `not found` and skips `create-release-tag`. `onlatest=true` is load-bearing: without it the suffix would skip `latest` and a bare `:latest` from that cell would overwrite the backend's moving tag. The `publish-manifest` concurrency group is per image (`-manifest-${{ matrix.image.name }}`): a job-level group without the matrix key is shared by backend, web and backup-agent, and GitHub cancels previously pending siblings when the next cell queues — they write different tags and must overlap; the per-image key is what still serializes two approved runs racing on the same image's `:latest`.

<a id="inv-098"></a>
### INV-098 — Vite dev is loopback-only by default.

`bun run dev` binds `127.0.0.1`; only `VITE_DEV_LAN=1 bun run dev` binds `0.0.0.0`. The Vite proxy reaches unauthenticated bootstrap routes, so CORS is not a substitute for this network boundary.

<a id="inv-099"></a>
### INV-099 — RustFS has no shipped secret.

`make env` generates independent 256-bit root/app secrets, persists them only in gitignored `.env` mode `0600`, and rotates the historical `rustfsadmin` / `foldex-change-me` placeholders without logging values. Compose requires both secrets on the service that OWNS the store (`docker-compose.services.yml`), while the backend's own copy in `docker-compose.yml` is `:-` because the object store is optional to it; backend and bootstrap reject either historical placeholder unless `RUSTFS_ALLOW_INSECURE_DEV_CREDENTIALS=1` explicitly opts into an isolated disposable dev setup. The app policy contains only bucket list/location plus object get/put/delete and required multipart actions — never `s3:*`, ACL or policy administration.

<a id="inv-100"></a>
### INV-100 — `.env` is never committed.

`.env.example` is the only canonical source.

<a id="inv-101"></a>
### INV-101 — Postgres credentials live in `POSTGRES_*` only — `DB_URL` is derived.

`docker-compose.yml` + `backend/Makefile` build the DSN. Override `DB_URL` only for external DBs (TLS, schema). If you change `POSTGRES_USER`/`PASSWORD` in `.env`, **delete any `DB_URL=` line**. `POSTGRES_HOST` accepts `db`/`localhost`/`host.docker.internal`/external — backend container's `extra_hosts` aliases `localhost` + `host.docker.internal` to host gateway.

<a id="inv-102"></a>
### INV-102 — Backup is a complete DB + RustFS snapshot ZIP

(`manifest.json` `Store`-compressed for client-side count read; `database.json` + `files/`; SHA-256 checksums; counts). Export runs under `REPEATABLE READ`. Detail: ADR-20 + `docs/SDD-BACKUP-RESTORE.md`.

<a id="inv-103"></a>
### INV-103 — Every backup operation is admitted before work; export and restore stream.

Export/validate/restore share one non-blocking slot acquired before export queries/spool creation or upload body reads/temp creation (`429 backup_busy` otherwise). `Export(ctx, w, onCountsReady)` emits `database.json` field/row at a time into a 0600/256 MiB spool with inline SHA-256, then streams the ZIP after commit. RustFS LIST metadata is callback-streamed and filtered against owner-scoped keys; only selected owner metadata plus manifest checksums remain O(owner-file-count), capped at 99,998 files, 1,024 bytes/key, 32 MiB of manifest, 64 MiB/file and 4 GiB expanded. Validate/restore run the same bounded archive preflight (100k unique entries; 32/256 MiB manifest/database; bounded collections; actual max+1 reads), and cleanup precedes slot release. Note media is validated/optimized once into a bounded operation spool before DB mutation and streamed from there after commit; ledger resumes optimize once on demand. Firefox/Safari use a same-origin native download backed by one 60-second, user/session-bound, one-time ticket; the GET consumes it before acquiring the same archive slot and invokes `Export` exactly once. Ticket state is process-local, capped at 128, request-pruned and retained 10 minutes only for owner-authenticated history polling (so a 15-minute access-token refresh cannot lose a longer export); restart invalidates it and multi-instance deployments require sticky routing for issue/download/status. **Never reintroduce a snapshot/ZIP `bytes.Buffer`** — a 2 GiB archive would peg the heap.

<a id="inv-104"></a>
### INV-104 — Backup restore is idempotent by default, never atomic across DB+RustFS.

Three modes: `wipe` (delete the CALLER's rows + object keys + prior restore ledgers, then re-insert with FRESH ids), `skip` (migration 000027 checkpoints exact archive digest + entity/note-file mappings owner-scoped in the SAME tx as content/associations/clicks; `files_completed_at` publishes only after object writes), `duplicate` (tags renamed `nome (N)`; folders always new; links with URL collision fall back to skip + warning). Retry after DB commit/file failure reuses the durable mapping; completed repeat performs no mutation or object I/O. Entity/slug work is staged set-based; object upload/delete has cancellation and concurrency 8.

<a id="inv-105"></a>
### INV-105 — Backup carries NO auth material, and restore always writes rows owned by the CALLER (ADR-30).

*Guards:* `TestCrossUser_RestoreIgnoresOwnerEmail`

No `app_user`, `session`, `totp_secret`, `recovery_code`, `api_token`, `invite`, `user_identity` or `password_reset` row is ever exported — the ZIP is a file users download and hand around, and bcrypt hashes plus live refresh tokens in it would turn a convenience feature into a credential-theft primitive. `user_id` is NEVER accepted from the ZIP (`json.Decoder.DisallowUnknownFields`): `Snapshot.OwnerEmail` is informational and a mismatch only produces a warning, which is what makes a hand-crafted backup unable to plant rows in someone else's account (`TestCrossUser_RestoreIgnoresOwnerEmail`). Consequences that bit once must not be re-introduced: **wipe mode no longer preserves ids**, and wipe deletes an **explicit list of the caller's flat object keys** rather than calling `DeleteObjectsPrefix`, which would destroy every tenant's files.

<a id="inv-106"></a>
### INV-106 — Public note-media references are read capabilities only, never write/delete authority (migration 000022).

Keys remain flat: `screenshots/{link_id}.ext` / `images/{link_id}.ext` derive ownership from the owner-scoped link row, while every NEW `notes/{uuid}.ext` upload first creates a `note_media(user_id, object_key, lease_expires_at)` row. `note_media_ref(user_id,note_id,object_key)` has composite FKs to both the note and media owner; Create/Update parse local URLs only to propose keys, then an owner-scoped `INSERT ... SELECT` claims those already owned by the caller. Delete, folder cascade, export and wipe authorize solely from `note_media`/`note_media_ref`, never from `body_html`; a public `/n/{slug}` may reveal a key and the exact `GET /api/files/notes/{uuid}.{ext}` read stays outside session auth, but the handler rejects non-canonical UUID names, traversal and link-derived keys with 404. This grants no mutation. Migration 000022 deliberately DOES NOT infer ownership for legacy keys from HTML: valid UUID keys remain servable and fail closed for delete/export. Pending or released media is removed by a bounded 100-object sweeper after its lease expires. Restore never writes the snapshot's public `notes/<uuid>` key: it generates a fresh UUID, rewrites `body_html`/`cover_url`, batch-inserts ownership/refs for the caller, and maps the ZIP bytes to the fresh key. Id-derived link keys still require `mapping.remapFileKey`, and `realignLinkImageURLs` points each restored link at its new id.

<a id="inv-107"></a>
### INV-107 — Backup endpoints require RustFS.

`/api/backup/*` is mounted only when the storage client came up. Without RustFS the backup would be silently incomplete; routes don't exist at all (404, not partial 200).

<a id="inv-108"></a>
### INV-108 — `preview.Worker.Enqueue` returns an error

(`ErrQueueFull` / `ErrStopped`). HTTP handlers and importer treat enqueue as fire-and-forget with `_ = w.Enqueue(id)` — the link row already exists and `requeuePending` picks stragglers up on next boot. Stop ordering: set `stopped atomic.Bool` first, then cancel, then `wg.Wait` — never close the jobs channel.

<a id="inv-109"></a>
### INV-109 — The `foldex-web` image NEVER ships a private TLS key.

`entrypoint-certs.sh` uses a volume-mounted pair at `/etc/nginx/certs/{cert,key}.pem` OR generates a self-signed ephemeral pair on boot. Baking a key into a public image is HIGH-severity (Trivy/Scout flag) — operators pulling it would share the same private key. Local dev: `make up` bind-mounts `./web/certs` from the gitignored host directory.

<a id="inv-110"></a>
### INV-110 — The change-check worker reuses the preview `Fetcher` — never duplicate SSRF guards.

`internal/changecheck.New` accepts a `Fetcher` interface; `main.go` injects `preview.NewFetcher`. Adding a second HTTP client would silently fork the SSRF posture. Fingerprinter's feed fetch goes through the same `GetRaw`. Detail: ADR-23.

<a id="inv-111"></a>
### INV-111 — `link.last_fingerprint` is prefixed `feed:<hex>` or `content:<hex>`.

The prefix is the **strategy discriminator**: kind switch `content:` → `feed:` is treated as new baseline, NOT change. `worker.process` only fires push when `prevKind == newKind && prevHash != newHash`. Without it, the first feed-augmented scan would always fire a false-positive push.

<a id="inv-112"></a>
### INV-112 — First observation never counts as a change.

`last_fingerprint IS NULL` → grave the new fingerprint without bumping `last_change_detected_at`. Without this, every newly opted-in link sends a "this page changed" push on its first scan.

<a id="inv-113"></a>
### INV-113 — Opt-out clears the full change-check column group.

When `LinkUpdate.CheckInterval` is `null` (tri-state), the repository writes `check_interval = NULL` AND `last_checked_at = NULL`, `last_fingerprint = NULL`, `last_change_detected_at = NULL`, `change_seen_at = NULL` in the same statement. Re-opting-in would otherwise replay a stale badge.

<a id="inv-114"></a>
### INV-114 — Manual `/api/links/{id}/seen-change` is a no-op when `last_change_detected_at IS NULL`

(404). Prevents out-of-band POSTs from permanently suppressing the next genuine detection.

<a id="inv-115"></a>
### INV-115 — `push_subscription.endpoint` is GLOBALLY UNIQUE; upsert is the only INSERT path.

`INSERT … ON CONFLICT (endpoint) DO UPDATE SET p256dh, auth, user_id` — the endpoint is a physical browser channel, and re-subscribing moves it to the currently signed-in owner instead of accumulating dead duplicates. Each owner is capped at 16 rows under an `app_user` row lock: refreshing an endpoint already owned by the caller remains valid at the cap, while another row returns `subscription_limit_reached`; `List` and `Notify` independently clamp fan-out to the same ceiling. Delivery always uses a strict public-only transport, independent of `PREVIEW_STRICT_SSRF`, with pre/post-dial private-IP checks and redirects disabled; capability URLs never enter logs. `/push/test` admits at most two concurrent fan-outs process-wide.

<a id="inv-116"></a>
### INV-116 — 404/410 from the push service removes the subscription row

(RFC 8030 §7.3). Other non-2xx are logged, row stays. Transport errors NEVER delete — a transient network blip would wipe live subscriptions.

<a id="inv-117"></a>
### INV-117 — VAPID private key is `0o600` and never baked into the image.

`internal/push.LoadOrGenerate` writes to `VAPID_STATE_PATH` (default `/data/vapid.json`) with explicit `os.WriteFile(..., 0o600)` — umask not trusted. Volume `foldex-data` persists; pin `VAPID_*` in `.env` for stable subscriptions across recreations.

<a id="inv-118"></a>
### INV-118 — Web Push delivery is decoupled but lifecycle-owned.

After a durable change-check CAS, `worker.process` admits the notification to a 32-message drop-newest queue consumed by fixed workers. Each `sender.Notify` inherits worker cancellation plus a 15s timeout, so slow push cannot roll back the result or outlive `Stop`. Detail: ADR-24.

<a id="inv-119"></a>
### INV-119 — Service Worker is hand-rolled — no `workbox-*` runtime deps.

`web/src/sw.ts` uses Cache API + raw fetch directly. `vite-plugin-pwa` with `strategies: 'injectManifest'` injects `__WB_MANIFEST` at build; runtime caching + Web Push event listeners (`push`, `notificationclick`) are hand-written. Adding workbox imports later requires bumping `bun.lock`.

## UI/UX (CLAUDE.md §5)

<a id="inv-120"></a>
### INV-120 — A mount-time request uses a ref guard and NO per-effect `alive` flag.

*Guards:* `strictMode.test.tsx`

`<React.StrictMode>` runs effect → cleanup → effect, so a one-shot ref makes the second pass return early and no new closure exists; the resolved promise is then judged against the FIRST closure whose cleanup already set `alive = false`, and nothing ever renders. `VerifyEmailScreen` and `EnrollTotpScreen` both shipped that way and both hung on a spinner forever — over a single-use token already spent and stripped from the URL, so a reload could not retry. Setting state after unmount has been a no-op since React 18. `renderWithProviders` does NOT wrap in StrictMode, which is why `strictMode.test.tsx` renders these screens itself.

<a id="inv-121"></a>
### INV-121 — Every dialog closes on `Esc`

via `useEscape(onClose, open)`. **No `window.confirm()`** — always `useConfirm({ title, message, destructive })`. Focus trap via `useFocusTrap(ref, open)` on every dialog (Tab/Shift+Tab cycle inside, focus restored on close).

<a id="inv-122"></a>
### INV-122 — Destructive actions

render with `fx-confirm-btn-danger` + trash icon + monospace `⚠ AÇÃO DESTRUTIVA` kicker. Cancel = ghost.

<a id="inv-123"></a>
### INV-123 — A NEW folder or tag opens on a SUGGESTED colour, and only a new one.

Every create used to start on the same indigo, so a library built by accepting the default is a wall of identical chips and the colour stops being information. `lib/suggestColor.ts` picks from `INLINE_PALETTE` but **skips a colour already in use while any unused one remains** — a blind `Math.random()` over twenty entries collides about half the time by the seventh tag. `taken` is compared through `primaryColor`, so a gradient counts as its first stop and cannot leave that stop looking free; once everything is taken it draws from the whole palette, which is the honest answer at that point. **EDIT never suggests** — repainting a folder someone opened to rename is a change they must notice to undo. The taken list is a SNAPSHOT read when the dialog opens and is deliberately NOT an effect dependency: as a dep, any refetch of the folder/tag list would overwrite a colour the user had just picked, with no visible cause. `TagPicker` also counts the chips added in the SAME dialog, or two pending tags created back to back could still collide. The `pick` argument exists so tests can pin the choice; the assertion that matters is that a fixed return fails four cases.

<a id="inv-124"></a>
### INV-124 — Inline tag creation in LinkDialog/NoteDialog is deferred and atomic with parent save.

Pending tags use `id: 0`; submit sends their definitions beside existing `tag_ids`, and the backend creates and associates them inside the parent link/note transaction so a failed parent write cannot leave orphan tags. Pending chips let the user cycle colors by clicking the dot (palette in `web/src/lib/inlinePalette.ts`, Tailwind 500-weight to minimize collisions).

<a id="inv-125"></a>
### INV-125 — LinkDialog auto-fills Title/Description from the URL after a 500 ms debounce

— only on **create** (edit mode skips entirely; the link already has its own copy), only when the field is **empty** (`setTitle((cur) => cur.trim() ? cur : next)` — user input always wins), and only when `looksLikeUrl(url)` passes. If the page returns an empty title, the hostname (`hostOf`) fills the field so the slug has something to derive from. Effect uses `AbortController` so a fresh keystroke cancels the previous in-flight fetch AND unmounting the dialog aborts cleanly (no setState on dead component). A spinner on the title label covers the debounce+fetch window. Failure is silent (no toast, no submit block) aside from the hint under an empty title. The dialog may **display** `og_image_url` from the same payload as a preview; the stored cover is still written by the preview worker (or a manual screenshot/upload). Screenshot capture lives as an icon on the image-panel header so it stays visible when the side column clips.

<a id="inv-126"></a>
### INV-126 — Tooltips are CSS-only via `data-tooltip` (+ optional `data-tooltip-side`)

*Guards:* `TooltipPortal.test.tsx`

rendered through `<TooltipPortal>` (portal to `document.body`, viewport-clamped). Never use native `title` on visible UI. Keep `aria-label` for a11y. **A press closes the chip, and it stays closed while the pointer remains on the control.** Clicking a tooltipped control almost always opens or changes something, and the chip is then painted over the result — the topbar avatar covered the first row of its own dropdown. Two wrong fixes were tried first and both are instructive: dropping `data-tooltip` while the menu is open STRANDS the chip, because `onOut` finds its anchor with `closest('[data-tooltip]')` and nothing matches once the attribute is gone; and closing on `mousedown` alone is not enough, because `close()` clears the current anchor while the pointer is still sitting there, so the next mouse event re-opens it 180 ms later — straight back over the menu. Suppression is keyed to the pressed element and cleared on `mouseout`, so an ordinary re-hover still works. `TooltipPortal.test.tsx` locks all four states, and the second wrong fix PASSED a test that only asserted the chip was gone right after the click.

<a id="inv-127"></a>
### INV-127 — Sidebar stays clean

— no per-row edit/delete. Editing goes through `TagManagerDialog` (opened by "Gerenciar tags" footer button). Collapsed sidebar = 44 px rail with expand chevron; state in `localStorage["foldex.sidebar.collapsed"]`.

<a id="inv-128"></a>
### INV-128 — Pinned links always come first.

`ORDER BY l.pinned DESC, ...` applies in every sort mode. Card shows gradient pin badge (always visible when pinned, on-hover when not).

<a id="inv-129"></a>
### INV-129 — Notes are interleaved with links in the same grid, not a separate section.

`Home`/`CardsView`/`ListView`/`CompactGrid` all read from `useEntries` (`web/src/api/entries.ts`), never a note-only query, so search/sort/pagination/pinned-first apply identically to both kinds. Default (non-alpha) sort renders folders, then entries in the backend's already-sorted order; alpha sort re-interleaves folders + entries by name via the shared `web/src/lib/mergeAlphaCells.ts` helper (used by all three view components — don't duplicate the interleave logic locally). `NoteCard`'s kind-discriminator badge (`.fx-card-note-badge`, emerald, `I.note`) sits at `top:10px; left:10px` — the opposite corner from the pin badge's `right:10px` slot — because it's present on every note card (not conditional like the pin/update badges), so it needs a collision-proof spot rather than another right-side offset.

<a id="inv-130"></a>
### INV-130 — Reading a note happens IN the app; `/n/{slug}` is the SHARE link, not the reader.

"Abrir" on a note card was an `<a target="_blank">` to the public page, so reading your own note meant leaving the app and landing on the same anonymous view a stranger gets from a share link. It opens `NoteViewDialog` now — title, tags, freshness, then the note. **The consequence that had to be handled, not just noticed: this records no view.** `click_log` is the single source of truth and the public `/n/` route is the only path that inserts into it, so the optimistic `click_count` bump in `onOpenNote` was REMOVED with the navigation — left in, it would show a count the server never took, which the next refetch silently corrects, so the number goes up and then back down on its own. The reader keeps a link to the public page for when a real visit is what you want. The card's affordance is a `<button>`, element-qualified in CSS for §5's usual reason.

<a id="inv-131"></a>
### INV-131 — Note `body_html` is user-authored rich HTML rendered raw (`template.HTML` / no client-side escaping) — the sanitization invariant in §4 is what makes that safe, not an assumption.

The Tiptap editor (`NoteDialog.tsx`) intercepts image paste/drop via `editorProps.handlePaste`/`handleDrop` and routes through `POST /api/notes/images` (returns a proxy URL) rather than letting a `data:` base64 blob land in the document — this matches the sanitizer's allowlist, which has no `data:` scheme. Drag-and-drop merge-to-folder is generalized across link↔link, link↔note, and note↔note (`MergeSource = {kind, id}` in `NoteCard.tsx`, threaded through `App.tsx`'s `onMergeEntries`) — a new card kind must extend this union, not add a parallel merge path.

<a id="inv-132"></a>
### INV-132 — Grid is row-major and density is user-controlled.

`.fx-grid` / `.fx-pingrid` use CSS Grid (never `column-count`). Density picker (3/5/8) lives in Topbar's `fx-viewseg`, visible only when `viewMode === 'cards'`, persisted in `localStorage["foldex.grid.cols"]` (default 5). Mobile breakpoints (≤980px / ≤640px) set a **lower cap** only.

<a id="inv-133"></a>
### INV-133 — Card preview area has a fixed height

(`.fx-preview { height: 150px; min/max-height: 150px }`). Images use `object-fit: scale-down` so large shrink to fit, small render natural size.

<a id="inv-134"></a>
### INV-134 — "preview falhou" hides when an image is already present.

Gated by `link.preview_status === 'failed' && !link.og_image_url`. With a working image, flagging "failed" alongside it is noise.

<a id="inv-135"></a>
### INV-135 — `localStorage` is the persistence layer for UI prefs

under `foldex.*` namespace, with SSR-safe `typeof localStorage !== 'undefined'` guard in the initializer.

<a id="inv-136"></a>
### INV-136 — `/go/{id-or-slug}` button label says "Acessar"

— never the implementation path.

<a id="inv-137"></a>
### INV-137 — The OTP field is six inputs, and every one of its keyboard behaviours is a contract.

*Guards:* `OtpInput.test.tsx`

Only the FIRST cell carries `autoComplete="one-time-code"` — Safari fills every annotated input with the same digit when autofilling from Messages, producing `111111`. `type="text"` + `inputMode="numeric"`, never `type="number"` (spinners, accepts `e`/`-`, drops leading zeros — fatal when `012345` is valid). Backspace clears the current cell and STAYS; only a second press steps back, or correcting one wrong digit would always destroy the one before it. Paste accepts `123 456`, `123-456` and `123456`. `onComplete` fires **exactly once** per code — a single-use code submitted twice always fails the second time and paints an error over a login that succeeded — and the verification screen stays `busy` after success, since the gate has not swapped it out yet. All locked by `OtpInput.test.tsx`.

<a id="inv-138"></a>
### INV-138 — The second-factor UI renders per METHOD, and never re-derives the server's policy.

The settings section lists authenticator × e-mail independently, and each disable button follows the server's `can_disable_*` answer rather than a rule recomputed in the browser. The e-mail method is offered only when `email_available` — the server refuses the enrollment on an instance whose driver prints mail to stdout, so a button there would be a promise the backend always breaks. `EnrollTotpScreen` (mandatory admin enrollment) asks WHICH method before starting anything, because minting a TOTP secret the admin then abandons for e-mail leaves a pending row behind — but it skips the chooser and starts the authenticator when `email_delivery` is false, since a question with one possible answer is pure friction on a screen the admin cannot leave. The mount ref-guard is keyed by METHOD and the "no skip" rule is unchanged. **A method's state, its action, and the reason an action is absent all live in the SAME row.** The settings card rendered them as one flat stack of 12px grey lines — status, both method names, the recovery count and the policy note all at identical weight — with `required_note` at the FOOT of the card, four lines below two methods it could not name. An admin therefore saw a missing button and, elsewhere on the screen, an unattached sentence; the report was "the buttons are blocked". The lock now sits inside the row it applies to, amber, and only when that method is ON: `can_disable_*` is also false for a method that is not enrolled, so a lock keyed on that alone would claim every unused method is protected. **The disable button is additionally gated on the method being enrolled** — that is not re-deriving the policy, which stays the server's answer; it is refusing to offer removal of something that is not there, and without it a row rendered "set up" and "turn off" side by side. An enrollment in flight and the one-time recovery codes each REPLACE the overview rather than appearing inside it: both are a single task with a single next action, and leaving the method list beside them offers ways out of a step the user has not finished.

<a id="inv-139"></a>
### INV-139 — Recovery codes are shown exactly once, behind an explicit acknowledgement.

The server keeps only a server-keyed digest, so it genuinely cannot show them again; the checkbox is what turns "we warned you" into "you confirmed". Copy and download are offered because the expected path is transcription onto paper.

<a id="inv-140"></a>
### INV-140 — All keyboard shortcuts are Alt-based.

`⌥K` palette, `⌥N` new link, `⌥F` new folder. Browsers swallow most `⌘`-modifier combos (⌘K, ⌘N, ⌘P). Any new SPA shortcut MUST use `alt+<key>`.

<a id="inv-141"></a>
### INV-141 — Pasting a URL anywhere opens the New Link dialog with it pre-filled.

Document-level `paste` listener (`web/src/hooks/usePasteUrl.ts`) uses `web/src/lib/url.ts:looksLikeUrl` (accepts `http(s)?://`, `ftp://`, `file://`, bare `example.com/x`; rejects words, numbers, multi-word, `mailto:`/`tel:`/`javascript:`). No-op when target is editable (INPUT/TEXTAREA/SELECT/contentEditable) or any `.fx-overlay` is mounted. `pastedUrl` MUST be cleared on close so subsequent `+ New link` clicks start empty.

<a id="inv-142"></a>
### INV-142 — Dark mode is neutral charcoal/slate, not purple.

Only the accent (`--fx-accent` indigo `#8B85FF`) carries hue.

<a id="inv-143"></a>
### INV-143 — Backup mode picker uses dual visual encoding

for `wipe`: red border + red background on the option AND `fx-confirm-btn-danger` on submit AND literal `⚠` prefix on the label. `skip` and `duplicate` use indigo accent. The submit gradient is what makes destructive intent unmissable.

<a id="inv-144"></a>
### INV-144 — Backup history persists in `localStorage["foldex.backups"]`

(array of `{id, created_at, duration_ms, size_bytes, counts}`, capped at 10). New entries prepend; other tabs sync via `storage` event.

<a id="inv-145"></a>
### INV-145 — The signed-in user is reachable everywhere via the topbar avatar menu (`UserMenu`).

It is the ONLY sign-out affordance in the authenticated app (auth screens aside) and deep-links to the hub's Profile section. Profile self-service is exactly TWO fields, both on `PATCH /api/auth/profile` (strict DTO, so role/status smuggling dies as `invalid_json`): the display name (trim + 120-char cap) and the **account language** (`app_user.locale`, mig 000035). The e-mail is NOT edited inline — it is changed through the two-step in ADR-41, which lives on the sign-in card as its own row, because it is the login identifier the password and Google rows qualify. Locale is **tri-state on both sides** — absent means *keep*, `""` means *follow my browser*, a value means *set* — and the field travels ONLY when it changed, because sending it on every save would let a plain rename overwrite a preference changed from another tab. It is validated with `mailer.LookupLocale`, which accepts what a browser actually sends (`pt-BR`, `PT`) and stores the normalized catalogue key; validating by `NormalizeLocale(x) == x` instead would reject those exact tags, since that function's job is to never fail a render, not to say whether it recognised anything. NULL means *no preference*, not English. The topbar picker and this field are ONE setting: `useAccountLocale` applies the stored preference on session load and the picker writes through to the profile, and a failed write reverts the picker immediately rather than leaving a choice that a later mount silently undoes. `useAccountLocale` READS only, and deliberately: adopting the displayed language for a NULL preference was implemented and then removed, because `""` is a real selectable value meaning *follow my browser* and the schema cannot tell it from *never chose* — so an unprompted write on mount undid that choice the moment it was saved. The `navigator.language` × `Accept-Language` divergence it was meant to fix is handled where it is born instead (§4's locale bullet: the anonymous flows send the displayed language as a HINT). **A locale-only write must send NO name** — `auth.updateLocale`, not `updateProfile` — because both fields are tri-state server-side and replaying a cached name reverts a rename made in another tab, which is the exact mirror of the hazard the locale field already guards against. "Sign out everywhere" (self `logout-all`) requires the destructive confirmation dialog.

<a id="inv-146"></a>
### INV-146 — Everything a user manages about THEMSELVES is one page, and the bands are not cards.

`AccountPage` absorbs what were four tiles — profile, sign-in methods, two-factor, API tokens — plus a sessions band, ordered by how often each is touched rather than by subsystem. Four tiles was four cards deep over content that was often a single line: the sign-in card rendered its password form only when the account HAD no password and its Google block only when the provider was configured, so an ordinary account with a password and no OAuth opened a card containing one status line and no action at all. A card implies an independent boundary and none of these are independent — the password and the Google link constrain each other (an account converted to Google-only cannot unlink until it has a password again), which is exactly what a boundary between two cards hides. The hero's chips report password and second-factor state so the account screen shows the state of the account without scrolling to the sections that change it. **One section shows at a time, chosen from a rail.** Stacking all five was the first shape and it did not survive contact: three screens of scrolling in a 760px column wasted the width AND made nothing findable — the two failures at once. The rail costs one click and makes "where is X" answerable by looking, which is what the four tiles got right and the stack gave up. It is also what gives the merged names a destination: `security` and `tokens` land on their own PANEL through `SECTION_TAB`, rather than resolving to "the account page" and arriving on Profile. `profile`, `security` and `tokens` remain ACCEPTED section names resolving to `account` through `canonicalSection`: the topbar user menu deep-links `profile`, and a link someone kept would otherwise land on the overview with no explanation.

<a id="inv-147"></a>
### INV-147 — Changing a password while signed in goes through `POST /api/auth/password/change`, and the CURRENT password is the whole step-up.

No second factor is asked for on that branch, deliberately: demanding one as well would lock out an owner who knows their password and cannot reach their authenticator. The CREATE branch is the opposite case and keeps §4's rule — with no current password to prove, the second factor is the only step-up, and it is gated on `HasSecondFactor()` (TOTP **or** e-mail), never on `totp_enabled` alone. The endpoint had existed since ADR-30, tested on the server, with NO caller in the SPA: changing a password meant signing out and using the reset-by-e-mail flow — a recovery path standing in for an ordinary edit, on an account whose owner is signed in and proving nothing.

<a id="inv-148"></a>
### INV-148 — The settings hub is the single consolidated settings/administration surface.

One gear button in the topbar opens it; import/export and administration have NO separate topbar destinations — the hub's shortcut tiles and RBAC segment replace them. Inside, a scope segment (`Personal` × `Administration`) exists **only for admins** (hidden, never disabled, for everyone else — mirroring the server, where `/api/admin/*` 404s). Tiles open detail sections with a back affordance; a mid-flight role loss (demotion) falls back to the personal overview instead of rendering denied surfaces. AdminUsersPage renders inside the hub and carries no pagehead of its own. The overview is a **full-width page** (`.fx-hub-page`, max 1240px, centred), not an 860px column — at 860 it hugged the left edge and left two thirds of a desktop viewport empty. Both scopes render the SAME card (`components/HubCard.tsx` → `.fx-acard`), so a spacing change cannot land on one grid and miss the other.

<a id="inv-149"></a>
### INV-149 — Every password input in the tree is `PasswordInput`, and the reveal toggle's three details are the contract.

`type="button"` (inside a form the default is `submit`, so the first click on the eye would post the login form), a **revealed field that suppresses spell-check** (`spellCheck={false}` plus the `autoCorrect`/`autoCapitalize`/Grammarly opt-outs — a `type="text"` field is fair game for Chrome's Enhanced Spell Check and editor extensions, which SEND its contents to a remote service, and mobile keyboards learn it into the personal dictionary; the values behind these fields include the master recovery password and folder unlock passwords) and **state that always starts hidden** — a revealed password must not survive a remount, which is exactly what a persisted preference would do on a shared screen. The toggle **is in the tab order** — keeping it out was tried and reverted, because a reveal a keyboard-only user cannot reach withholds it from the people most likely to need it to check a long password they cannot see. It carries **no default `className`**: eight call sites put `.fx-input` on a wrapper div and nest a bare input, so a default drew a second bordered pill inside the first. `aria-label` says what the NEXT click does and is the whole affordance: `data-tooltip` is deliberately absent because `TooltipPortal` mounts only inside the signed-in shell, so on the auth screens it would render nothing. Twenty-one call sites, one component, because these are details that are easy to get wrong once and impossible to notice.

<a id="inv-150"></a>
### INV-150 — `.fx-auth-input` is `box-sizing: border-box`, explicitly.

Nothing under `.fx-auth` inherits the `.fx-shell *` border-box default, so `width: 100%` plus padding on a content-box input made EVERY auth field 28px wider than its own column. It went unseen for as long as the pane had slack to absorb it, and became visible the moment the password field needed room for a reveal toggle.

<a id="inv-151"></a>
### INV-151 — A row action shows its LABEL, not only its glyph.

The administration table's five per-row actions shipped icon-only for one session and the instance owner reported disable, sessions, recovery and delete as MISSING FEATURES — four working actions read as decoration at the edge of the row. A tooltip is not discovery: it requires already suspecting something is there. Icon plus text now, `aria-label` still naming the ACCOUNT as well as the action so two rows never read the same, and the action phrase stays CONTIGUOUS in that label (`Sign out everywhere ({{email}})`, never `Sign {{email}} out everywhere`) — interpolating into the middle breaks both a reader scanning and a test querying by name. The two destructive ones keep their confirmation dialog.

<a id="inv-152"></a>
### INV-152 — "Remember my e-mail" stores the ADDRESS and nothing else, writes only after the credentials are ACCEPTED, and unticking ERASES.

It is `localStorage['foldex.auth.email']` rather than an account field for the obvious reason — the whole point is to have it before anyone is signed in. Writing before the server accepts would remember a typo the user is still correcting; a box the user unticks while looking at their own address, that then hands the same address back next visit, has done the opposite of what it says. The password is never stored, and there is no session-lifetime component to this: it is a convenience on the form, not a credential.

<a id="inv-153"></a>
### INV-153 — The confirmation modal's card needs BOTH `fx-confirm` AND `fx-modal`.

`overrides.css` styles only the pair (`.fx-confirm.fx-modal`); `.fx-confirm` alone has no rule anywhere, so a dialog built with one class renders as unstyled controls spilling across the viewport — which is exactly what shipped once. A side-task form is a **drawer** instead (`.fx-drawer-scrim` / `.fx-drawer`): anchored right, full height, an OPAQUE background rather than a surface token (the scrim behind it is dimmed content, and a form for typing a credential must not have text showing through), a body that scrolls independently so the actions stay reachable, and dismissal only from a mousedown that both starts and ends on the scrim — one that drifts out of the panel while selecting text must not discard a half-typed credential.

<a id="inv-154"></a>
### INV-154 — A card that is a `<button>` MUST be styled through an element-qualified selector.

*Guards:* `scripts/test-css-button-reset.mjs`, `scripts/test-nginx-headers.sh`

`.fx-shell button { border: 0; background: transparent; padding: 0 }` (foldex.css, near the top) is `(0,1,1)` and therefore **out-specifies any bare component class** `(0,1,0)`: a `<button className="fx-acard">` silently loses its background, border and padding and renders as unpadded text floating on the page gradient. This is not hypothetical — it is what made the whole settings hub look unstyled, and it is the same reason `.fx-topbar .fx-cta` exists in `overrides.css`. Write `button.fx-acard` (ties at `(0,1,1)`, wins on document order) or scope with a second class. Hover/focus rules are already `(0,2,0)` and need nothing. Lowering the reset with `:where()` is NOT the fix: existing rules deliberately rely on it beating a component class. **This trap has now shipped twice**, and the second time it was invisible in a way the first was not: `.fx-hub-seg-btn-active` and `.fx-vs-active` were bare, so the settings hub's scope segment and the topbar's view segment rendered their ACTIVE option byte-for-byte identical to the inactive one — no pill, no accent, no padding — which is the one thing a segmented control exists to show. `.fx-side-row` and `.fx-topbar .fx-seg` escaped only because `overrides.css` happens to re-declare them chained. **No unit test can see this**: the markup was always correct (class applied, `aria-pressed` set), jsdom loads no stylesheet, and `vitest.config.ts` sets `css: false`, which resolves a `?raw` CSS import to the EMPTY STRING — a Vitest guard was written first and went green against the broken file. `scripts/test-css-button-reset.mjs` reads the CSS itself and runs in CI, the same shape as `scripts/test-nginx-headers.sh`. **The same trap had two older victims that nobody had noticed**, both surfaced by putting their controls beside styled ones on the account page: `.fx-btn`/`.fx-btn-primary` had NO rule anywhere in either stylesheet despite fourteen usages, so every action in the two-factor and API-token sections rendered as bare text; and `.fx-input` applied DIRECTLY to an `<input>` (rather than to a wrapper div, which is the other shape it is used in) lost its border and background to `.fx-shell input`. Both are now element-qualified and in the guard's list. **`.fx-btn-danger` was the fourth**, found the same way one redesign later: no rule in either stylesheet, two call sites, and both of them the button that REMOVES a second factor — so the destructive action rendered identical to the neutral one beside it. The class family is the pattern to distrust: a variant that only ADDS to a base class is the easy one to add markup for and forget CSS for, and nothing fails when you do. It is in the guard's list now, like the rest.

<a id="inv-155"></a>
### INV-155 — Every `*attemptlimit.Limiter` on `Handler` MUST be in `limiters()`, and a reflective test is what says so.

*Guards:* `TestSweepLimitersEvictsEveryBucket`

`attemptlimit` counts CONSECUTIVE failures — except on a key written through `CommitFailFor`, where the cap is measured against the number of DISTINCT MEMBERS the key has accumulated (`login:ip:<addr>` is the only such key today, and it counts accounts, per SDD-ABUSE-DEFENSE §4.2). The sweep argument is unchanged and applies to both: the set dies with the entry it hangs off, and a bucket outside the sweep keeps a set that only ever grows. `fails` is cleared by `CommitSuccess`, or by `clearExpiredLocked` once a lockout has already fired, or by `Sweep` evicting an idle entry. A bucket whose call site never has a success — the availability probe charges every answer — therefore has only the third path, so leaving it out of the sweep does not merely leak an entry: `fails` climbs monotonically for the life of the process, and an account that edits its username a few times a year eventually meets a five-minute lockout it did nothing to earn, with the screen saying only "could not check". `TestSweepLimitersEvictsEveryBucket` seeded four of ten buckets and asserted the literal `Equal(t, 4, …)`, so it stayed green through six additions and would have stayed green through this one. Deriving the count from `limiters()` is NOT enough either — that was the first fix, and removing a bucket from the slice kept it green, because it sweeps whatever the slice happens to hold. The test now **reflects over the Handler's fields** and requires the slice to hold every one. A test whose name promises completeness has to derive completeness.

<a id="inv-156"></a>
### INV-156 — Anything portaled to `<body>` leaves `.fx-shell`, and therefore leaves everything the shell establishes.

*Guards:* `scripts/test-css-portal-menu.mjs`

`.fx-shell` is not just a background: it declares `box-sizing: border-box` for every descendant, the UI font, the 14px/600 type scale, AND the `.fx-shell button` reset above. A `createPortal` to `document.body` — which the topbar's menus need, because the topbar's `overflow: hidden` would clip an in-flow dropdown — lands outside all of it. The user menu was written as if it were inside: its items got the UA's button border, background and typeface, and `content-box` made `width: 100%` plus `padding: 8px 10px` overflow the 6px-padded container they sat in. The result looked like nothing else in the product, on the one control that is reachable from every screen. **The inverse of the trap above, and it hides better**: there the classes were bare and lost a specificity fight, here the CSS was correct and the ANCESTOR it depended on was absent — so reading the rule tells you nothing, and the orphan-class guard sees a class that is properly declared. **A portaled panel also needs an OPAQUE background, which no `--fx-surface-*` token gives it.** All of them carry alpha (0.62 → 0.94) and read solid only where something opaque sits behind — true inside the shell, false for a menu floating over arbitrary content, where at 0.78 the cards underneath were legible through the user menu. The fix is compositing, not a darker token: `linear-gradient(var(--fx-surface-3), var(--fx-surface-3)), var(--fx-bg)` keeps the tint and the edge blur while the panel itself stays readable, in both themes. `.fx-portalmenu` re-establishes the context and every portaled root wears it; `scripts/test-css-portal-menu.mjs` (in CI, with a self-test) refuses a `createPortal` whose subtree contains a `<button>` and whose root does not. **`.fx-overlay` is the SAME defect through a different door, and the portal guard cannot see it**: it is a SIBLING of `.fx-shell` under `#root`, so no `createPortal` is involved, and every modal rendered there is outside the shell. Measured on the folder-unlock prompt — `inShell: false`, the bare input kept its native `2px inset` border and white background (a second bordered pill inside `.fx-input`), and `content-box` made it 482px wide inside a 460px container, which is what pushed the reveal eye out of the field. Both classes now carry the box-sizing, the type scale AND the `input`/`button` element resets, and the guard checks all three for each — because the type scale alone leaves the native chrome, which is the half that actually looks broken. A portal with no button — a tooltip, a read-only popover — is exempt, since it carries no control the missing reset could disfigure. `.fx-usermenu-item` is also element-qualified and declares its own `border`/`background`/`font-family`, because outside the shell there is no reset to inherit from. The LocalePicker escaped the visible half only because it sets those properties inline; it was missing `font-family` and `box-sizing` all along.

<a id="inv-157"></a>
### INV-157 — An IDENTIFIER is not a preference, and the account page splits on that line.

The username shipped on the Profile form beside the display name and the language, and the instance owner asked why it was not under Access — correctly: a username is typed into the login screen exactly where the e-mail goes, and `verifyPassword` resolves ONE identifier against both columns. Filed under "profile" it reads as a nickname, which is also why nobody thinks to look for it there. It now sits in **Sign-in**, between the e-mail and the password, with its own `SectionRow`. It writes through `auth.updateUsername`, which sends the username ALONE — the same rule as `updateLocale` and for the same reason: every field on `PATCH /api/auth/profile` is tri-state, so a row replaying a cached display name would revert a rename made in another tab. Clearing it is offered in the same row, because a username that cannot be removed is not optional, only deferred.

<a id="inv-158"></a>
### INV-158 — Every account panel is ONE card shape, and every message it says back is ONE component.

`components/account/SectionCard.tsx` holds `SectionCard` / `SectionBadge` / `SectionBlock` / `SectionRow` / `Notice`; profile, sign-in, two-factor and sessions all compose them. It began as the two-factor card and lived there alone for one session — long enough for the other three to look broken beside it: profile was an unframed form, sign-in two loose rows, sessions a sentence with two buttons. They were not styled differently on purpose; they were styled by nobody. **A row is a `<div role="group" aria-label={name}>`, never a list item** — each is a heading plus the controls acting on that one thing, so "Password, group" is the useful announcement and `within(row)` is what lets a test (or a reader) tell which "Turn off" belongs to which method. A row states its own STATE (`state`), the reason an expected action is ABSENT (`lock`, amber, inside the row), and information (`note`, grey) — the three were previously one undifferentiated grey line at the foot of the card. **`Notice` carries the live region per TONE**: `bad` is an `alert`, `ok` a `status`, `info` neither, because `info` explains something that was always on screen and announcing it interrupts for no event. Generic parts are `fx-sec-*`; only the proof panel, the OTP row and the enrollment key keep `fx-2fa-` names. `.fx-sec-row-form:empty { display: none }` is load-bearing: `children` is a Fragment and always truthy, so a closed form otherwise draws its rule and accent bar under every row; and the width cap goes on `.fx-sec-row-form > *`, never on the wrapper, since `flex-basis: 100%` is what breaks the form onto its own line and a `max-width` there lets it fit beside the row again — parking the whole form at the right edge.

<a id="inv-159"></a>
### INV-159 — A class in the markup with no rule behind it is a broken screen, and `scripts/test-css-orphan-classes.mjs` is what refuses it.

*Guards:* `scripts/test-css-orphan-classes.mjs`

Six occurrences before the guard existed: `.fx-btn` and `.fx-btn-primary` (fourteen buttons as bare text), `.fx-inline-error` (four credential refusals as unstyled prose), `.fx-btn-danger` (both "remove a second factor" buttons identical to the neutral one), and `.fx-acct-row .fx-auth-oauth`, which stopped matching the moment the row class was renamed. Nothing in the tree can see it: TypeScript sees a string, jsdom loads no stylesheet, and `css: false` makes a `?raw` import the empty string — only a person who already knows what the screen should look like. The check is ONE-directional (an unused CSS rule is dead weight; an unstyled class is a defect), skips chunks adjacent to a `${…}`, and carries a `KNOWN_ORPHANS` baseline of nine pre-existing cases on screens nobody was changing — **that list may only shrink**, and `.fx-error` on the instance-policy form (a refused save as plain text) is the one worth fixing first.

<a id="inv-160"></a>
### INV-160 — `.fx-auth` is an OVERLAY, and only a signed-out screen may wear it.

*Guards:* `scripts/test-css-auth-overlay.mjs`, `TwoFactorSection.test.tsx`

`auth.css` declares it `position: fixed; inset: 0` with an opaque background; `AuthShell` and the `AuthGate` boot screen ARE that surface. Everything it styles that the signed-in shell also needs — the OTP field, the QR, the recovery-code grid — is scoped `.fx-auth .thing`, so reaching those used to mean putting the overlay class on a div inside a card and cancelling `position`/`padding`/`background` by hand. **That cancellation held only because it was an INLINE style.** Rewritten as a class in `foldex.css` it lost to `.fx-auth` on document order — `auth.css` loads last and both are `(0,1,0)` — and the wrapper became a full-size opaque sheet painted over the card: every label, heading and row background gone, the layout intact underneath, and a repaint storm (a fixed, `overflow-y: auto` box inside `.fx-card`'s `backdrop-filter` containing block) that blocked the main thread, so the screen flickered and never settled. It shipped. `.fx-authfield` now carries the `--fxa-*` aliases and answers the same descendant selectors via `:is(.fx-auth, .fx-authfield)` — same specificity, no layout — and is the only correct wrapper inside the shell. The recovery-codes panel had worn the bare overlay since before that change, uncancelled, on a screen shown exactly once. **Two guards, because each sees a half the other cannot:** `scripts/test-css-auth-overlay.mjs` (in CI) refuses the class in any `className` outside the two allowed files AND checks that `.fx-authfield` still declares the tokens and still matches the cell selector; and a jsdom assertion in `TwoFactorSection.test.tsx`, because `css: false` hides the stylesheet from Vitest but **not the class name** — that assertion needed no CSS and would have caught this.

<a id="inv-161"></a>
### INV-161 — Every signed-OUT screen carries a flag row, and it is mounted on `AuthShell`, never per screen.

Every auth surface renders through that frame, so mounting the switcher there is what makes "no screen can be missed" structural rather than a checklist. It is a ROW, not the topbar's dropdown: a user who lands on the reset or invite screen may be reading a language they do not speak, and a menu labelled *Language* in that language is a control they must guess at before they can open it. **The flag is decorative and the language name is the accessible name** — a flag is a country, not a language, and Windows draws regional-indicator pairs as bare letters — so the code stays visible beside the glyph and the control still works when the emoji does not render. `useLocaleChoice` is the ONE place a pick is applied, shared with the topbar picker: the account write-through is the half that must not diverge, because without it `useAccountLocale` re-applies the old preference on the next load and silently undoes a choice the user watched take effect. The write is skipped with no session, which is every login screen.

<a id="inv-162"></a>
### INV-162 — Locale picker lives in the topbar.

Persists to `localStorage["foldex.locale"]`. Default detection: `navigator.language` falling back to `en`. Adding a new locale = drop JSON in `web/src/i18n/locales/`, list in `SUPPORTED_LOCALES`, populate every key from `en.json` (source of truth).

<a id="inv-163"></a>
### INV-163 — Monitored / unseen-change UI.

Cards with `check_interval IS NOT NULL` always render a "Monitored" chip. Cards with unseen `last_change_detected_at` render an amber badge (`fx-card-update-alert` + bell icon); clicking it calls `useMarkChangeSeen` optimistically. Sidebar's "Recent updates" section refetches every 60 s, cap 10 items.

<a id="inv-164"></a>
### INV-164 — Push subscription UI is a bell in the Topbar.

Four states: unsupported / denied / off / on. Hooks `useWebPush`/`useSubscribePush`/`useUnsubscribePush` wrap the `PushManager` plumbing — components never touch the API directly.

<a id="inv-165"></a>
### INV-165 — Mobile responsiveness

(3 breakpoints in `web/src/styles/foldex.css`): ≤980px / ≤640px = grid caps to 2/1 cols; ≤768px = topbar single-row, sidebar off-canvas, FAB for new-link; ≤600px = dialogs full-screen, inputs min-height 44px, safe-area inset bottom. **Gotcha**: `overrides.css` loads after `foldex.css` — every desktop-only rule there MUST be wrapped in `@media (min-width: 769px)` or mobile breaks silently. Detail: ADR-22.

<a id="inv-166"></a>
### INV-166 — A password an administrator installs is either CONFIRMED or GENERATED, never typed once.

`POST /api/admin/users` is the one route where somebody chooses a credential for another
person (INV-021), and it was the one password field in the product with no confirmation.
That is the worst possible place for the omission: every other single-field password is
one the typist can recover — they know what they meant. Here a typo is installed on an
account the typist does not own, and the person it belongs to learns about it only as a
sign-in that fails, with no reset link (the address is created UNVERIFIED on purpose) and
no way to tell a wrong password from a wrong address. The recovery is a second
administrator action.

**Generating is offered because confirming a value you had to invent is the wrong ask.**
`lib/generatePassword.ts` draws from a 57-character alphabet with `0`/`O` and `1`/`l`/`I`
removed — the value is TRANSCRIBED (read out, written down, pasted into a chat) and a pair
that survives that trip is worth more than the ~0.2 bits per character the excluded symbols
add. At length 20 it still carries ~116 bits. Bytes outside the largest whole multiple of
the alphabet are **discarded, not folded in with `%`**: 256 % 57 = 28, so a plain modulo
would make the first 28 letters ~1.3× likelier. `generatePassword.test.ts` drives the
generator with a stub alternating one rejected byte with one accepted byte and asserts the
exact output, so a modulo implementation fails it rather than merely looking uniform.

**The length comes from the INSTANCE floor, not the compiled-in one.** The floor is
owner-configurable (ADR-35, up to 128), so a fixed 20 would be a button the server always
refuses on a hardened instance — the same defect as offering the e-mail factor where
`MAIL_DRIVER=log`. The dialog reads `GET /api/admin/policy` (admins may READ it) and the
button is disabled only while that query is PENDING; a FAILED query falls back to the
compiled-in floor and still generates, because refusing forever over a network blip is
worse than a value the server might refuse once.

**A generated value drops the confirmation field and gains a plaintext band.** There is no
typo to catch, and asking for a 20-character random string twice is friction with no
defect behind it. It has to be readable, or the administrator cannot hand it over — and it
gets a band of its own rather than relying on `PasswordInput`'s reveal toggle, because that
toggle deliberately forgets its state on every remount (INV-149). Editing the field by hand
revokes the generated status, so the confirmation returns; without that, a value the
administrator half-retyped would ship unconfirmed.

**The client length gate is capped at 72, and that cap is what keeps the server's refusal
reachable.** `MaxPasswordFloor` is 128 while `MaxPasswordLen` is 72 BYTES (bcrypt's
truncation point), so a floor above 72 is unenforceable and no password can satisfy it.
The first version gated the submit on the raw floor, which turned that into a DEAD BUTTON:
generate produced 72 characters, the client refused them, and nothing was sent — so the one
answer that names the real number, and thereby reveals the misconfiguration, was
unreachable. The gate is `min(floor, 72)`; the server says `password_too_short` with its
own number. Same reasoning as showing the SERVER's message rather than a client constant.

**The floor is stated on screen.** A gate that can refuse for a reason no constant on the
screen knows must say what that reason is, or the administrator sees a disabled button and
reports a broken feature.

**The strength checklist is hidden over a generated value.** It is guidance for a person
INVENTING a password; over 116 bits of randomness it reports only a missing symbol class,
which reads as the product marking its own output deficient.

**The plaintext band carries `translate="no"`.** Chrome and Edge page translation POST the
page's visible text to `translate.googleapis.com`. An `<input>` value is exempt — which is
why [INV-149](#inv-149) needs nothing for this — but a text node holding a credential is
exactly the shape that gets sent, and this product ships pt/es precisely for the population
whose browser auto-translates. The TOTP seed, the API-token plaintext and the recovery-code
sheet were retrofitted with it in the same change; they had carried the exposure since they
shipped.

**What foldex does NOT control, and the README says so:** on submit this is an ordinary
password field, so `autoComplete="new-password"` stops autofill but not the browser's
*save* prompt — the administrator's own vault may keep the target user's temporary password
— and the clipboard copy persists until something replaces it. That is the same window
[INV-021](#inv-021) already declares; the invitation flow is what avoids it.

<a id="inv-167"></a>
### INV-167 — The RBAC matrix is CONFIGURABLE, and a compiled floor is what the configuration cannot reach.

ADR-42. `role_permission` (mig 000039) holds the editable half; `internal/roleperm` resolves
it as the UNION of that table with a floor the table cannot touch. Each floor rule exists
because without it the configurability itself would be unsound:

- **The `owner` never reads the table.** Its grants come from the compiled map on every
  resolution, which is what guarantees that no state of that table — truncation included —
  produces an instance with nobody able to repair it. The CHECK constraint refuses an `owner`
  row, so the direct-SQL path cannot reach it either.
- **`roles.assign` is unlockable.** It is the META-permission: a role that could be GIVEN the
  power to grant would grant itself everything else in one further step, which makes locking
  the rest decorative.
- **`policy.write` and `instance.transfer`** are ADR-35's reasoning: an admin who took
  `policy.write` would lower the password floor and walk in through it.
- **`content.read` is locked in the OTHER direction** — it cannot be removed. An account that
  cannot read its own library is broken, not restricted, and its owner cannot tell which.

A locked permission is read from the compiled map **whatever the row says**, which is what
makes the lock a guarantee rather than "the current screen does not offer it".

**The write is bounded by the CALLER, not by a list.** `ValidateWrite` refuses to grant what
the writer's own role does not hold. That is the rule answering "an administrator must not
give itself owner-level powers", and it is stated against the caller on purpose: a permission
unlocked later is covered by construction, where a list would have to be remembered. Revoking
is deliberately NOT bounded that way, or an admin could never undo a grant the owner made.

**The gate takes the matrix as a PARAMETER.** `authgate.RequirePermission(grants, p)`, not
`RequirePermission(p)`, and that is the whole safety of the ADR: as a package-level default, a
mount site that forgot to pass the configured matrix would silently keep enforcing the
compiled one, and an owner's revocation would appear to save while changing nothing. As a
parameter, forgetting is a compile error — the compiler is what listed the seven sites.
A `nil` matrix at the GATE denies; `nil` at a CONSTRUCTOR means the compiled matrix, so the
substitution is visible where it is chosen rather than buried in the gate.

**`Can` runs on every request, so resolution is a snapshot**, not a query: a read per check
would put a round trip in front of every API call. A failed reload keeps the previous
snapshot — replacing it with the compiled matrix on every failure would silently restore
permissions an owner deliberately revoked. `StartReloading` (30 s) bounds how long a
revocation takes to reach a replica that did not perform it, and how long the process that
DID perform it serves a stale matrix when its own post-write refresh fails — without it
that second case is forever, and it fails OPEN.

**The gate must hold the LIVE store, never a snapshot.** `Mount` runs once at boot, so a
handler capturing `Grants()` there freezes every route it mounts on the matrix as it stood
at startup. That shipped once (`AdminHandler`) and was the same defect the gate's parameter
exists to prevent, one level up. `liveGrants()` returns the repository; `grantsSnapshot()`
is per request and is never captured at Mount. **And `cmd/server/main.go` must set
`Deps.Grants`**: the nil-means-compiled default exists for tests, and leaving it in force in
production makes a revocation commit, audit, render as unticked and change nothing on
`/links`, `/notes`, `/tags`, `/import` and `/backup/restore` — while the gates main wires by
hand still honour it, so it looks partially applied rather than broken. Guard:
`TestServerDepsCarriesTheLiveGrants`, which walks main's AST, because the defect is one
absent struct field in a literal that compiles.

**Every permission in the vocabulary is gated by some route.** Three were not —
`backup.export`, `invites.read`, `invites.write` — which was a documentation gap while the
matrix was compiled and becomes a toggle that saves, audits and does nothing once it is
editable. `content.read` is the single exemption and it is CHECKED, not asserted: the guard
requires it to stay locked and held by every role, so unlocking it starts demanding a gate
on the same commit. Guard: `TestEveryPermissionIsEnforcedSomewhere`.

**`ValidateWrite` checks the caller BEFORE the loop.** Every other rule sits inside the loop
over `want`, so an empty set skipped the caller entirely and `Set(viewer, admin, nil)`
stripped every admin to its locked floor. Unreachable over HTTP, but this function is
documented as the choke point a second entry point cannot get past.

**One writer per role at a time.** DELETE-then-INSERT under READ COMMITTED loses a
revocation — the second transaction snapshots before the first commits, so rows the first
inserted survive its DELETE, and an owner sending `[]` concurrently with an admin sending
`["content.write"]` leaves the role holding it: the merge of two intents that sending the
FULL set exists to prevent. A `pg_advisory_xact_lock` keyed by role, not SERIALIZABLE:
there is nothing to retry, and the second writer waiting is the wanted behaviour.

**`testdb.Reset` RESEEDS it.** Truncating and stopping would leave not a clean database but an
instance with every editable role stripped to its locked floor, and the next test to build a
real repository would watch ordinary content writes answer 403 with nothing naming the cause.

<a id="inv-168"></a>
### INV-168 — Papéis e permissões is a GRID, and every cell it cannot offer says why.

Permissions are ROWS and roles are COLUMNS, because the question the screen is opened with is
"who can do X" — one row — not "what can role Y do". It was four stacked chip lists, and a
chip list can only show what a role HAS: "denied" and "does not exist" rendered identically,
which is the one distinction a permissions screen exists to make.

Everything about what may be edited comes from the SERVER (`editable`, `locked`, `can_edit`,
`caller_role`). Re-deriving any of it — `role !== 'owner'` is the tempting one — would be two
copies of one authorization policy, and §5's rule applies: the copy that drifts either hides a
control the server would allow (reads as a missing feature) or offers one it refuses.

A blocked checkbox carries the REASON as its tooltip, and the four reasons are different
sentences: your role cannot edit at all; this column is the owner; this permission is not
configurable anywhere; you cannot grant what you do not hold. One generic "disabled" would
leave an administrator with no way to tell which.

The cell's accessible name carries BOTH axes (`content.write for Editor`), or all fifty-six
checkboxes announce the same thing. The permission id renders as the raw string the server
uses, with the prose beside it and not instead: an administrator comparing this against the
API docs needs the same token in both places. A permission the server sends and this file has
no group for still renders, under `other` — a vocabulary the server grew must not VANISH from
the one screen whose job is to show what the server enforces.

<a id="inv-169"></a>
### INV-169 — The password floor's write bound is bcrypt's 72 bytes, and its READ bound is not.

A password is hashed as at most 72 BYTES, so a floor above that is a rule no password can
satisfy: every creation and reset answers `password_too_short` forever, and the most hardened
instance is the one that cannot install a credential at all. `MaxPasswordFloor` is therefore
72, enforced by `ValidateForWrite`.

**The READ bound stays at the historical 128, and that split is load-bearing.**
`policy.Repository.Get` falls back to `Default()` on a failed `Validate`, so tightening the
read bound would make an instance configured at 100 silently resolve to the baseline — floor
8, AND an empty Google allowlist, AND the default OTP lifetime. A rule getting STRICTER must
never be the thing that switches the other rules off. The stored value is honoured as written;
the owner is refused only when they next SAVE, which is the moment they are looking at the
field. `WarnUnenforceableFloor` logs it at boot, because otherwise such an instance refuses
every password and nothing anywhere says why.

The client gate mirrors this: `CreateUserDialog` gates on `min(floor, 72)` so the server's
refusal — the only answer that names the real number — stays reachable. See
[INV-166](#inv-166).

<a id="inv-170"></a>
### INV-170 — A request span identifies its caller by OPAQUE ID ONLY, and the annotation hangs off principal CREATION, not a route group.

`tracing.AnnotatePrincipal` stamps exactly three attributes on the SERVER span: `user.id`
(the numeric account id — `span.user.id` in TraceQL), `user.roles`, and `foldex.auth.via`
(`session` or `api_token`). No e-mail, no display name, no `session_id`.

**A trace store is a different retention domain from the database.** It has its own access
control, its own retention window, and a copy in every backend that scrapes it — an address
written into a span outlives the account it belonged to and lands somewhere the tenancy rules
of §4.1 do not reach. An opaque id is worthless to anyone who cannot already read `app_user`,
which is exactly the property that makes it safe to export. Same reasoning as the raw URL path
that never becomes a span name.

`TestAnnotatePrincipalRecordsNoIdentifyingAttributeBeyondTheOpaqueID` asserts this NEGATIVELY —
any `user.*` / `enduser.*` key outside the allowed three fails, so widening the set is a
deliberate decision rather than a one-line drive-by.

**`user.name` is REMOVED from the pool's query spans, because it names a different subject.**
`otelpgx` stamps semconv's `user.name` with the POSTGRES ROLE on every CLIENT span it emits.
semconv has one such key and this application has two things that fit it: the role the pool
authenticates as, and the account whose request is being served. Side by side in one trace —
`user.id` on the SERVER span, `user.name` on its children — they read as the id and the name of
the same person, and they are not. The failure is silent and convincing rather than loud: a panel
grouping "requests by `user.name`" answers `user_foldex` for 100% of traffic and looks like a
working breakdown, which is worse than having no panel at all. `internal/db` therefore passes
`otelpgx.WithDisableConnectionDetailsInAttributes()` and re-adds the same set minus that key via
`connAttrs` — `server.address`, `server.port`, `db.namespace` stay, because those vary per
deployment and answer "which database did this span talk to"; the role does not vary and is
already in `DB_URL`. `TestQuerySpansNeverCarryThePostgresRole` runs a real query under a SERVER
parent and asserts across EVERY recorded span; the fast `TestConnAttrs_*` beside it pins the set
without Docker but cannot catch the option being dropped, which is why both exist.

**It is a function called from the three principal seams, and the first draft proves why.**
`Authenticate`, `Optional` and the `AUTH_ENABLED=0` bootstrap are the only places a principal is
established; the annotation is called at each. The first version was a MIDDLEWARE mounted on the
`/api` group instead — and it silently missed the whole authenticated half of `/api/auth`
(sessions, password change, 2FA, API tokens), which is precisely the credential-management
surface an operator most wants attributed. Nothing failed: no build error, no panic, an identical
response. A group mount annotates the group it was mounted on; a seam annotates every identity
that exists. `TestAuthenticate_StampsUserIDOnSpansOfTheAuthSurfaceItself` and
`TestOptional_StampsUserIDWhenASessionIsPresentAndNothingWhenItIsNot` are that defect's tombstone,
and both are integration tests through a real session — a unit test composing the chain by hand
stays green through the mutation.

**A FOURTH seam is refused by an AST guard.** Moving the call onto the three seams fixed the
instance, not the class: a seam added later — an OAuth callback, a public-token gate — would
reintroduce the identical hole, and the per-seam integration tests can only prove the seams that
exist today. `TestEveryPrincipalSeamAnnotatesTheSpan` walks production `internal/**` for every
`authctx.WithPrincipal` call and requires `tracing.AnnotatePrincipal` in the same enclosing
function (`authctxtest` allowlisted — it never runs inside a request). It matches on the AST call,
never on file text, for the reason `TestEveryPackageUsingTestdbStopsIt` documents: a text-matching
guard flags its own comment, and a guard that fails for the wrong reason teaches people to route
around it.

An unauthenticated request carries NO `user.id`, never `"0"`, which would collapse every
anonymous trace onto one fictional account. With `AUTH_ENABLED=0` every request is attributed to
the bootstrap administrator and its spans read `owner` — the escape hatch working as documented.

**`user.id` must never become a Tempo span-metrics dimension.** High cardinality is correct on
a span attribute and is one time series per account on a metric; the dashboards keep deriving
RED from `http.route`. See [INV-070](#inv-070).

**Exporting identity transfers privilege.** The OTLP exporter speaks PLAINTEXT, UNAUTHENTICATED
gRPC unless the endpoint carries an `https://` scheme, and the sampler records every SERVER span —
so on a cleartext link a passive observer reads per-request account id, role and credential kind.
Whoever can read the trace store can enumerate which accounts hold `owner`/`admin`, which use API
tokens, and profile any single account's activity, holding no Foldex permission at all. **Trace-store
read access must be treated as an administrative privilege of this instance**, and a collector off
this host must use `https://`. There is deliberately no identity-only kill switch today: an operator
who cannot accept the egress turns tracing off.

<a id="inv-171"></a>
### INV-171 — As credenciais do S3 externo existem SÓ no processo do backup-agent, e o dump operacional é cifrado por DEFAULT.

*Guards:* `TestLoad_PlaintextIsAnExplicitOptOutNeverAFallback`, `TestLoad_RefusesToBootHalfConfigured`, `TestDumpRun_ShipsAnEncryptedVerifiableArtifact`, `TestAgent_HeartbeatNamesTheExternalDestination`, `TestAgent_HeartbeatCarriesNoCredential`

O dump da instância (ADR-43) carrega TUDO — hashes bcrypt, conteúdo de todos os usuários — e o destino é um bucket fora da máquina. Dois muros seguram isso: (1) `BACKUP_S3_*` entra apenas no serviço `backup` do compose; o backend, processo exposto à web, pode INSERIR uma linha `requested` em `backup_run` e nada mais — um RCE no backend não ganha escrita no bucket de backup. (2) A cifragem client-side (age/X25519) é default-obrigatória: sem `BACKUP_AGE_RECIPIENTS` o agente recusa o boot, a menos do opt-out nomeado `BACKUP_ALLOW_PLAINTEXT=1` para quem cifra no próprio bucket. O caminho de upload não carrega segredo nenhum (cifra com chave PÚBLICA); a identidade privada vive num cofre fora do host — não há autogenerate de propósito, porque uma chave que só existe ao lado dos dados é um backup indecriptável no dia em que o host morre. O `artifact_sha256` registrado é do CIPHERTEXT, verificável contra o bucket com `sha256sum` sem decifrar nada.

**Corolário: o ENDEREÇO do bucket é publicado; o ACESSO a ele, nunca.** O heartbeat carrega por job um `destination` — `{endpoint, bucket, prefix}` — e a tela de agenda o renderiza. Isso não afrouxa o muro (1): o backend continua sem credencial nenhuma e continua só podendo inserir uma linha `requested`. E é a metade que faltava para o operador conseguir CONFERIR a configuração — "copia os objetos para o bucket externo" não nomeia bucket nenhum, e o endpoint é justamente o campo que mais aponta para lugar diferente do pretendido: apontado para a mesma máquina da origem, o espelho não sobrevive a nada que ele existe para cobrir, e nada na tela dizia isso. A separação é mecânica, não editorial: os três campos publicados são os que não são segredo, e `TestAgent_HeartbeatCarriesNoCredential` lê a linha do heartbeat crua e recusa qualquer uma das quatro chaves (`BACKUP_S3_*`, `RUSTFS_*`) dentro dela — porque tudo o que esse payload carrega está numa tela.

<a id="inv-172"></a>
### INV-172 — Um job de backup roda no máximo uma vez por vez, e a exclusão tem TRÊS camadas que se cobrem.

*Guards:* `TestBegin_TheRunningSlotIsExclusivePerJob`, `TestClaimRequested_CASPromotesExactlyOnce`, `TestExpireStale_FreesTheSlotADeadAgentHeld`, `TestAdvisoryLocks_CrossProcessCoordination`

O índice parcial único (`backup_run_one_running_idx`, `WHERE status='running'`) é a verdade PERSISTIDA: dois agentes por erro de deploy não registram o mesmo job nem que nunca tenham se visto. O advisory lock (`InstanceBackupAdvisoryLockKey`, "FOLDXBKP") é o mutex de EXECUÇÃO, liberado por queda de conexão. E o janitor expira `running` velho para `failed('stale_claim')` — sem ele, um agente morto no meio de um run segura o índice para sempre e o job nunca mais roda, em silêncio. Cada camada cobre a falha das outras duas; remover qualquer uma reabre ou o double-run ou o deadlock eterno. O botão "Executar agora" respeita as mesmas camadas: o backend INSERE `requested`, o agente promove por UPDATE condicional (CAS), e a promoção também colide no índice parcial.


<a id="inv-173"></a>
### INV-173 — A env decide QUAIS jobs de backup existem; o banco decide QUANDO rodam; e pisos compilados impedem a agenda de cair abaixo do mínimo.

*Guards:* `TestValidateJobConfig_FloorsHold`, `TestEffectiveTiming_MirrorRowCannotSwitchTheMirrorOn`, `TestSchedule_WriteIsOwnerOnlyThroughTheLockedPermission`, `TestAgent_SyncPicksUpScheduleRowsLive`, `TestAgent_HeartbeatCarriesTheEnvBaseline`, `TestNormalize_LegacyRowBecomesTheUnifiedShape`, `TestMigration_AgreesWithNormalizedOnEveryLegacyShape`, e no front `reseeds the draft when the env baseline arrives after the first fetch` / `widens an env baseline below the job floor, keeping its times`

A agenda configurável (ADR-44, vocabulário revisado pelo ADR-45) só é segura por causa desta divisão. **Capacidade vem da env** — credenciais (`BACKUP_S3_*`, `RUSTFS_*`), a identidade age do drill, o interval>0 que constrói o client do mirror — porque uma linha de `backup_schedule` não conjura um segredo dentro do processo do agente; uma linha para um job incapaz é logada e ignorada, e o heartbeat (`backup_agent_state`) diz o PORQUÊ à UI em vez de deixar a agenda configurada e ignorada para sempre (a lição do mailer). **O quando vem do banco**, num documento com UMA forma para os quatro jobs (`{mode, times[], weekdays[], interval_min, enabled}`, `mode` explícito e nunca inferido), dentro de pisos compilados em `backupagent.ValidateJobConfig` aplicados nos DOIS lados (PUT do backend e load do agente, a mesma função — uma linha escrita por SQL direto degrada à baseline, nunca é meio-honrada):

| job | pode desligar? | modos | horários | dias mínimos | intervalo |
|---|---|---|---|---|---|
| `dump` | não | times, interval | 1..6 | **5** | 15..1440 |
| `drill` | não | times, interval | 1..6 | 1 | 15..1440 |
| `mirror` | não | times, interval | 1..6 | 1 | 15..1440 |
| `user_zip` | **sim** | times, interval | 1..6 | 1 | 15..1440 |

O piso do dump é maior que o dos outros de propósito: o dump é a proteção da instância, e os demais jobs são verificação (drill), cópia contínua (mirror) e conveniência de produto (user_zip). O ADR-45 nomeia o degrau que esse piso cedeu — era "dump todo dia", virou "dump em ≥5 dias" — em troca de a agenda poder dizer "segunda, quarta e sexta"; toda mudança que REDUZ a frequência passa pelo diálogo de confirmação do INV-122, calculado sobre disparos por semana, não sobre a string de display do agente. **A escrita é owner-only pela permissão TRAVADA `instance.backup_schedule`** — o argumento do `policy.write` aplicado a DR: um admin que pudesse esticar a agenda do dump ou estacionar o drill afinaria o disaster recovery e agiria dentro da brecha. **Linha ausente = baseline da env**, e o heartbeat publica essa baseline ESTRUTURADA (`JobReport.baseline`), não só renderizada: é o que faz a promessa "a env é a primeira opção, o banco é a sobrescrita" existir na tela em vez de só no documento — sem ela o formulário semeava com constantes e a env não era opção nenhuma. Deletar uma linha nunca desliga proteção, só devolve o job ao horário que a env sempre teve.

**Corolário do formulário: a tela nunca abre numa BASELINE que o servidor recusaria.** A env é isenta dos pisos — ela É a baseline —, então `BACKUP_DUMP_AT="03:30 sun"` é legal na env e ilegal como linha. Semear o formulário com ela produziria um 400 garantido no primeiro Salvar, e semear com constantes da tela produz o defeito oposto: quem salva sem tocar em nada grava a opinião da tela por cima da agenda da env. A regra que resolve os dois: semeia da linha, senão da baseline do heartbeat, senão do fallback compilado; o que a linha não disser é completado (o rascunho é GORDO de propósito, e `payloadOf` canonicaliza no envio, para que trocar de modo ou religar o `user_zip` nunca abra campos vazios); e uma baseline abaixo do piso é ALARGADA para os sete dias mantendo os horários, porque essa é a direção que AUMENTA a proteção. Há uma segunda correção automática, e ela vai para o outro lado: uma lista de horários acima do teto é APARADA na semente — não só no render, senão a tela mostra seis e o servidor recusa nove, uma recusa que o operador não consegue atender. Por reduzir, ela passa pela confirmação do INV-122 como qualquer outra redução. As duas correções valem para a BASELINE e para o teto; uma LINHA escrita à mão abaixo do piso de dias não é alargada — ela é editável e o servidor a recusa com a mensagem que nomeia o número, que é o comportamento certo para um documento que alguém escreveu de propósito. A chave de remontagem do editor inclui a CHEGADA da baseline: sem isso o primeiro fetch com `agent: null` semeava do fallback e nenhum poll posterior corrigia.


### INV-174 — Uma troca de tela DENTRO de um fluxo nunca apaga a tela que já está no vidro.

*Guards:* `AuthGate.transition.test.tsx` — `asks for the code screen while the sign-in form is still on screen`, `keeps the sign-in screen on the glass while the code screen loads`, `swaps in the code screen once it arrives`, `asks for the recovery screen while the sign-in form is still on screen`, `keeps the sign-in screen on the glass while the recovery screen loads`

O `AuthGate` carrega seis telas por `lazy()`, e o fallback do `Suspense` é o `boot` — `.fx-auth fx-auth-boot`, uma sobreposição de viewport inteira. Para três delas isso é honesto: `ResetScreen`, `VerifyEmailScreen` e `ConvertScreen` são alcançadas no MOUNT, a partir de um token na URL, quando ainda não há nada pintado e o spinner de chunk é indistinguível do probe do `/me`.

**A linha que separa os dois grupos não é "token na URL" — é "havia algo pintado?".** As outras três são alcançadas por uma TRANSIÇÃO a partir do formulário já pintado, e são dois estados de UM fluxo: `TwoFactorScreen` e `EnrollTotpScreen` a um submit de distância, `ForgotScreen` a um clique — este último por `useState` local do próprio gate (`setForgot`), sem nenhuma navegação envolvida, e foi o caso que a primeira versão desta regra classificou errado justamente por procurar o token em vez do que estava no vidro. Como atualização urgente, o React commita o fallback, e o operador vê a página inteira piscar entre "Entrar" e o pedido do código: o card não trocou, a tela apagou. Duas regras, e as duas são necessárias:

1. **Toda troca que traz uma tela lazy sobre uma pintada é uma `startTransition`.** É o que faz o React SEGURAR a tela anterior enquanto a nova suspende, em vez de commitar o fallback. Sem isso nenhum prefetch resolve o caso ruim — um chunk lento continua apagando a tela. Para as telas que vêm de uma troca de SESSÃO fica em `applySession`, o ponto único por onde login, convite, reset e o próprio 2FA passam — mas **só para o status `two_factor_required`**, e o estreitamento é obrigatório: o `queryClient.clear()` logo acima é NÃO-escopado, e o argumento que o torna seguro é que a árvore desmonta no MESMO commit, sem deixar observer vivo para refetchar no cache esvaziado. Esse argumento só vale enquanto a atualização é urgente. O desafio é o único status que jamais coincide com um clear — ele não carrega user id, e o estado que ele substitui também não, então `lastUserId` não se move e o ramo do clear não roda (fixado por `does not wipe the query cache on the way into the challenge`). Nenhum outro status suspende — as árvores de login e do app são eager —, então nenhum outro tinha o que ganhar em ser adiado. A recuperação de senha não passa por `applySession` — é `useState` local do gate — e por isso declara a sua própria transição no `onForgotPassword`.
2. **Os três chunks alcançáveis a partir do formulário são aquecidos enquanto ele está na tela.** É o que torna a espera imperceptível em vez de apenas invisível. O segundo fator é o DEFAULT (`AUTH_REQUIRE_2FA_FOR_ADMINS`), então quem está olhando o formulário está a um submit de uma dessas telas — ou a um clique da recuperação; pagar o chunk ali é o que colocou o spinner no meio do fluxo. Quem já está autenticado nunca chega ao efeito — que é exatamente a visita para a qual o split foi feito, e por isso ele continua valendo.

Prefetch sozinho NÃO basta e o motivo é mecânico: `React.lazy` chama a própria fábrica no primeiro render e anexa um `then`, então mesmo com o módulo em cache ele suspende ao menos uma vez, e nada garante que o React não pinte o fallback antes do microtask resolver.

### INV-175 — `audit_log.subject` é CONTEÚDO, e existe exatamente UMA consulta que o lê.

*Guards:* `TestAuditSubjectIsSelectedByExactlyOneQuery`, `TestAudit_AdminNeverSeesAContentSubjectOrItsActorEmail`, `TestCrossUser_OwnActivityNeverReturnsAnotherAccountsRows`, `TestAudit_OwnFeedReturnsTheSubject`

O ADR-46 fez a trilha cobrir conteúdo, e com isso `audit_log` passou a ser a única tabela fora das tabelas de conteúdo que guarda conteúdo: `subject` é o título do link, o nome da pasta — o que o dono digitou. O INV-045 diz que conteúdo é privado por conta, **administradores inclusive**, e a trilha é lida por administradores. As duas coisas só coexistem porque a coluna tem **um** leitor.

Esse leitor é `ListOwnActivity`, escopado em `actor_id = o chamador` — INV-001 aplicado a uma tabela que é, no resto, de instância inteira. A projeção administrativa (`adminProjection`) não seleciona `subject`, e apaga `actor_email`, `target_email` e `detail` das linhas de conteúdo **dentro do SELECT**, com `CASE WHEN action = ANY($content)`; o administrador recebe `actor_ref`, um id opaco. Fazer isso em SQL e não em Go é a diferença entre uma garantia e uma convenção: um filtro aplicado depois que a linha chegou sobrevive até alguém adicionar a coluna "para o CSV ficar mais rico".

**Nenhum revisor humano pega a regressão**, e é por isso que o guard varre o AST. A coluna entraria por uma mudança de projeção num arquivo, e o vazamento apareceria numa tela em outro; quem revisa qualquer uma das metades não vê nada errado. O guard falha se **qualquer** declaração de produção — função **ou constante de pacote** — nomear `subject` num SELECT sobre `audit_log`. A primeira versão só andava por `FuncDecl` e passou com a coluna adicionada direto na constante `adminProjection`: a edição mais provável de causar o vazamento era exatamente a que ele não enxergava.

**O que o `actor_ref` protege.** Exposição INCIDENTAL — nomes ao lado de atividade de conteúdo numa tela aberta por outro motivo — e a possibilidade de correlacionar linhas entre si sem nomear ninguém. Não é defesa contra um administrador determinado: a administração entrega ids de usuário em outros lugares porque precisa, e numa instância self-hosted quem tem shell tem o banco. O cartão "contas mais ativas" nomeia contas e deliberadamente **não** carrega id, então não serve de tabela de-para. Qualquer coisa mais forte seria decoração, e o ADR-46 diz isso com essas palavras.

**A categoria falha para o lado do conteúdo.** `AuditCategory` devolve `content` para uma ação que ninguém classificou: o custo de errar assim é uma linha faltando numa tela, e o custo do outro default é dado de outra conta. A pertinência é um mapa fechado, não um teste de prefixo — `backup.restored` é conteúdo e `backup.run_requested` não é, e uma regra por prefixo poria os dois do mesmo lado enquanto parecesse ter decidido.

### INV-176 — `ip`, `ip_trusted` e `user_agent` são um CONJUNTO; um endereço sem procedência é a coluna que a 000033 recusou.

*Guards:* `TestAudit_StoresTheAddressWithItsProvenance`, `TestAudit_AMalformedAddressLosesTheColumnNotTheRow`, `TestAudit_TruncatesTheDeviceStringRatherThanFailing`

A migração `000033` recusou uma coluna `ip` com um argumento correto: `X-Forwarded-For` é forjável num bind direto, e uma coluna alimentada por ele seria **autoritativa na aparência e controlada pelo atacante ao mesmo tempo** — a pior combinação para uma tabela consultada durante um incidente. O que esse argumento proíbe é uma coluna que não sabe dizer de onde veio o valor. Registrar o endereço que o servidor de fato observou, ao lado de uma flag dizendo se um proxy configurado respondeu por ele, é outra coisa.

Por isso as três colunas andam juntas e `AuditRecord.WithRequest` é o único ponto que as preenche: copiar duas das três linhas num call site novo recria exatamente a forma recusada. `ip` é o `RemoteAddr` **depois** de `server.trustedProxyRealIP`; `ip_trusted` só é verdadeiro quando aquele middleware reescreveu o valor a partir de um par dentro de `TRUSTED_PROXY_IPS` (INV-007). A flag viaja pelo contexto (`auditctx.MarkTrustedIP`) porque uma linha adiante o `RemoteAddr` já foi sobrescrito e não há mais como saber. Sem proxy configurado toda linha é o par cru e a flag é falsa — honesto, e a tela renderiza a diferença em vez de escondê-la.

Um endereço que não parseia vira **NULL, nunca um INSERT recusado**: uma falha de auditoria é logada e engolida, então uma linha rejeitada não apareceria como erro — a ENTRADA sumiria, que é o único desfecho que uma trilha não pode ter. `normalizeAuditIP` também tira porta, colchetes e colapsa IPv4-mapeado, porque duas grafias do mesmo host fariam o bloqueio e o agregado de origens discordarem de si mesmos. `user_agent` é cabeçalho controlado pelo cliente, capado em 256: o caminho de login falho aceita linha de qualquer chamador não autenticado, e o balde de rate limit é chaveado pelo endereço tentado — uma string nova compra orçamento novo.

### INV-177 — A cobertura de conteúdo é um MIDDLEWARE; o rótulo é opcional por construção.

*Guards:* `TestContentAudit_RecordsAnAcceptedMutation`, `TestContentAudit_IgnoresRejectedWrites`, `TestContentAudit_TreatsAnImplicitWriteAsSuccess`, `TestContentAudit_IgnoresUnmappedRoutes`, `TestContentAuditActions_AreAllKnownContentActions`

Uma chamada de auditoria em cada handler é cobertura que depende de todo autor lembrar, e os buracos dela são invisíveis: uma entrada faltando parece um dia quieto. O middleware é o mesmo raciocínio que põe a redação de credenciais no handler raiz de log em vez de em cada call site.

O mapa é **fechado** e chaveado pelo **padrão de rota** do chi (`"POST /api/links/{id}"`), não pelo caminho — um mapa por caminho ou erraria toda linha ou precisaria do seu próprio matcher. Rota ausente do mapa não grava nada, de propósito: uma rota nova deve ser uma decisão sobre se pertence à trilha, não uma entrada automática sob um nome que ninguém traduziu. Só **2xx** é gravado — uma escrita recusada não mudou nada, e uma trilha que listasse tentativas ao lado de efeitos tornaria "quem apagou isto" insolúvel na tela que existe para responder isso. O `statusWriter` trata escrita implícita como 200 porque esse é o caminho de sucesso ORDINÁRIO, e expõe `Unwrap` para que uma resposta em streaming (o export de backup passa por este grupo) continue podendo dar flush.

O rótulo chega por `auditctx.SetRequest`, um holder mutável que o middleware instala — porque só o handler sabe que aquele POST criou a linha "ADR-46 rascunho", e devolver isso como valor de retorno significaria mudar toda assinatura de handler do produto por uma anotação opcional. **Um handler que nunca anota ainda produz a linha** e perde só a coluna do rótulo: a cobertura da trilha não depende de ninguém lembrar, e esquecer degrada uma coluna em vez de derrubar o evento. Nos deletes o rótulo é lido ANTES da exclusão — um instante depois ele não existe em lugar nenhum, e "link excluído #42" é uma entrada que o dono nunca consegue resolver.

### INV-178 — O bloqueio permanente de IP é owner-only e TRAVADO, seus trilhos são a segurança da feature, e o cache falha ABERTO.

*Guards:* `TestValidateBlockIP_RefusesTheAddressYouAreCallingFrom`, `TestValidateBlockIP_SelfRailSurvivesRespelling`, `TestValidateBlockIP_RefusesLoopbackAndUnspecified`, `TestValidateBlockIP_RefusesATrustedProxyByNetwork`, `TestBlocklist_FailsOpenWhenTheLoadErrors`, `TestBlocklist_KeepsTheLastGoodSnapshotOnFailure`, `TestAuditAPI_BlockRailsAnswerWithTheReasonTheyFired`

`instance.ip_block` é o argumento do `instance.backup_schedule` levado à borda de rede, um passo além: é a **única permissão cujo mau uso deixa a instância inalcançável, inclusive para quem a detém**. Uma permissão concedível de decidir quem alcança a instância poderia ser usada para trancar o owner fora da tela que a revogaria. Daí travada e no assento que não pode ser trancado do lado de fora do próprio instance.

O modo de falha desta feature **não é "um bloqueio que não funciona"** — é uma instância que ninguém alcança, instalada pela pessoa que mais precisava alcançá-la, por um botão colocado ao lado de um número vermelho assustador. Não há desfazer de fora: o endpoint de desbloqueio está atrás da mesma tranca. Cada trilho de `ValidateBlockIP` existe por isso:

- **o próprio endereço**: bloquear de onde você está conectado encerra a sessão que removeria o bloqueio. É o trilho que dispara na prática, porque o operador investigando uma rajada está com frequência atrás do mesmo NAT que ela. A comparação é entre endereços **normalizados** — um operador em IPv6 cujo endereço chega como `::ffff:203.0.113.9` enquanto a tela oferece `203.0.113.9` está a uma grafia de se trancar fora.
- **loopback**: é como um operador local administra a instância, e é o caminho de acesso INTEIRO com `AUTH_ENABLED=0`. Bloqueá-lo remove a escotilha que existe exatamente para este tipo de lockout.
- **proxy confiável**: atrás do nginx toda requisição chega do endereço do proxy sempre que a cadeia de encaminhamento não está como se pensa — e a tela teria acabado de mostrar esse endereço como a origem mais movimentada, que é precisamente o que convida ao clique. O teste é por REDE, não por igualdade de string: a configuração aceita CIDR.

Cada um responde com **seu próprio código** (`block_self`, `block_loopback`, `block_proxy`, `block_full`) e com a frase que descreve a situação do OPERADOR. "Endereço inválido" na frente de um controle que pode tornar a instância inalcançável não é algo sobre o que agir.

**O cache de enforcement falha ABERTO**, e é a inversão deliberada da regra usual. Isto é um filtro de incômodo, não uma fronteira de autenticação — quem decide o que alguém pode fazer é o middleware de sessão. Falhar fechado transformaria uma oscilação do banco numa indisponibilidade total, e entre os trancados estariam as pessoas que poderiam consertá-la. Um refresh que falha **mantém o snapshot bom anterior** e faz backoff pelo TTL, para não virar uma tentativa de recarga por requisição em cima de um banco que já está sofrendo. `/healthz` é isento: uma instância que se declara doente porque alguém bloqueou o endereço de uma sonda seria reiniciada em laço.

### INV-179 — Ordenação crescente na trilha é uma CONSULTA diferente, nunca a página invertida.

*Guards:* `TestAudit_AscendingOrderPagesFromTheOtherEnd`, `TestParseAuditFilter_OrderIsAPreferenceNotAFilter`, `asks the server for the other end rather than reversing the page`

A paginação é por keyset, e um cursor significa "continue depois desta linha" — de que lado é "depois" depende da direção, então `Ascending` inverte o cursor junto (`id > $n` em vez de `id < $n`). Inverter o array no cliente ordenaria só as cinquenta linhas já carregadas: "mais antigos" mostraria os mais antigos **dos mais recentes**, um controle que parece funcionar e responde outra pergunta. O mockup fazia exatamente isso sobre dez linhas de fixture, o que é aceitável num mockup e não sobrevive à paginação real.

`order=asc` é uma **preferência, não um filtro**: qualquer outro valor cai no default em vez de recusar a página. Um filtro desconhecido muda quais linhas voltam e por isso é 400 (`action`, `category`, `window`); uma ordenação desconhecida não muda o conjunto, só a apresentação.

### INV-180 — O balde do dia é construído no BANCO; a data local do processo não é a data das linhas.

*Guard:* `TestAuditStats_DaysIncludeTheEmptyOnes`, `TestAuditStats_DaySeriesAreDisjointAndComplete`

O gráfico diário usa `generate_series` porque um dia sem eventos não tem linhas para agrupar, e um gráfico que descarta dias vazios comprime uma semana quieta numa que parece movimentada.

O limite superior da série é o `now()` **do banco**, dentro do SQL, e nunca um `::date` sobre um parâmetro vindo do cliente. Um rascunho anterior escreveu `generate_series($1::date, $2::date, ...)`, o que fez o Postgres inferir os parâmetros como `date`: o driver então codificou o dia de calendário **LOCAL do processo**, enquanto `created_at` é comparado no fuso da sessão. Num servidor em UTC-3 o último balde terminava onde o dia começava, e todo evento do dia corrente caía **depois do fim do gráfico** — a coluna mais movimentada lia zero. A falha é silenciosa, só aparece fora do UTC, e parece "um dia quieto". Por isso o limite fica dentro do SQL, onde o relógio e as linhas são o mesmo relógio.

As quatro séries (`logins`, `failed`, `admin`, `content`) são disjuntas e juntas cobrem o vocabulário, então a soma delas é o total do dia e a coluna empilhada é honesta.


### INV-181 — A cota da API autenticada é por PRINCIPAL, conta só escrita, e não isenta ninguém.

*Guards:* `TestAPIQuota_TwoPrincipalsHaveIndependentBudgets`, `TestAPIQuota_TheOwnerIsNotExempt`, `TestAPIQuota_ReadsNeverConsumeTheWriteBudget`, `TestAPIQuota_ConcurrentRequestsCannotExceedTheBudget`, `TestExpensiveRoutes_EveryPatternNamesARouteTheRouterMounts`, `TestWiring_APIQuotaRefusesAWriteLoopThroughTheRealRouter`

Uma sessão válida não tinha limite nenhum contra um pool de 16 conexões (`internal/db/db.go`). É a lacuna G2 do `docs/SDD-ABUSE-DEFENSE.md` e o vetor de "DDoS" que de fato existe nesta arquitetura: um usuário hostil — ou o script bugado de um legítimo — satura a instância e todos os outros tenants sentem.

Cada linha do desenho responde a um modo de falha concreto:

- **Por PRINCIPAL, nunca por rota.** Por rota, um laço espalhado por vinte endpoints fica dentro do limite em cada um e mesmo assim segura o pool inteiro. O pool não sabe qual URL o esgotou.
- **A chave é a CONTA**, não a sessão nem o token. Um token de API é uma credencial de uma conta, não uma segunda conta: chavear por `token_id` faria de emitir um token a maneira de multiplicar o orçamento.
- **Só métodos mutantes.** Ler é um `SELECT` indexado; contabilizá-lo gastaria o orçamento de quem navega a própria biblioteca sem tocar na amplificação de escrita que a cota existe para conter.
- **Rotas caras têm um segundo balde, menor e por hora** (import, export e restore de backup, screenshot, refresh de preview), num mapa fechado por `METHOD + padrão de rota` — o mesmo vocabulário de `contentAuditActions`, pelo mesmo motivo do INV-177: cobertura que depende de alguém lembrar tem buraco invisível. Como a decisão precisa acontecer ANTES do handler (uma cota que decide depois do trabalho feito é um relatório, não um controle), o padrão é casado contra o path por segmento, não lido de volta do `chi.RouteContext`, que só é preenchido durante o roteamento.
- **Uma requisição RECUSADA não custa orçamento nenhum.** A rota cara paga os dois baldes; se o segundo recusar, o token do primeiro é devolvido. Sem isso, encostar num teto drena o outro, e quem estourou o limite por minuto perderia também os imports da hora seguinte.
- **429 com `Retry-After`, nunca desconexão.** O chamador é legítimo até prova em contrário e precisa poder recuar; uma conexão que só morre ensina o laço de retry a bater mais forte. O `Retry-After` é o tempo real de recarga arredondado para CIMA — um cliente que espera exatamente o que lhe disseram tem de encontrar um token lá, ou o 429 vira um laço.
- **O owner NÃO é isento.** Uma isenção seria uma conta capaz de derrubar a instância, e a primeira pessoa a tropeçar nela seria o próprio operador rodando um import grande.
- **Memória limitada, com sweep próprio.** O balde é um token bucket com teto de chaves e sweep dentro do próprio `Allow` — o INV-155 existe porque um balde deixado de fora da lista de sweep de outra pessoa só cresce e nada avisa; aqui não há lista externa para se ficar de fora. O sweep nunca encurta a janela: derrubar um balde que ainda deve tokens devolve exatamente o orçamento de que o chamador deveria estar sem.

O `attemptlimit` **não serve** e a justificativa é obrigatória porque o repo tem regra contra segunda implementação paralela: ele conta FALHAS CONSECUTIVAS e `CommitSuccess` zera o contador, então uma requisição aceita apagaria o registro inteiro; não tem recarga, então não distingue rajada legítima de laço; e o `Begin`/`CommitFail` existe para segurar orçamento durante um hash lento, que aqui não existe. `internal/pkg/quota` é um token bucket, que é outra coisa.

Uma política nula (`Deps.AbusePolicy == nil`) enforça os **defaults compilados**, nunca "sem limite": dependência não fiada que desliga uma defesa em silêncio é o modo de falha do INV-177.

### INV-182 — O clique público é coalescido em MEMÓRIA, por visitante hasheado, e a supressão nunca alcança o redirect.

*Guards:* `TestClickCoalesce_ARepeatVisitWritesOneRowAndStillRedirects`, `TestClickCoalesce_DifferentVisitorsEachWriteARow`, `TestClickCoalesce_ZeroSecondsDisablesCoalescing`, `TestClickCoalesce_TheMapNeverExceedsItsCeiling`, `TestWiring_RepeatClicksFromOneVisitorWriteOneRowAndStillRedirect`, `TestAllow_WithoutAGateRecords`, `TestEveryClickLogInsertIsGatedByAllow`

`/go/{slug}` e `/n/{slug}` não pedem sessão e cada acerto escreve em `click_log` (lacuna G3). Um laço sobre um slug conhecido é escrita ilimitada no banco por um anônimo.

- **Sem coluna nova e sem migração.** O estado de dedup é EFÊMERO: um mapa em memória de `(entity_kind, entity_id, digest do visitante)` com teto. Persistir o IP do visitante numa tabela de cliques é uma decisão de privacidade que remover um amplificador de escrita não precisa tomar — e contagem de clique é métrica de produto, não contabilidade: perder o segundo acerto do mesmo visitante em dez segundos não muda nada que alguém leia.
- **O identificador é um digest COM CHAVE do endereço**, com chave aleatória por processo, nunca o endereço em claro. O endereço vem de `clientIP(r)` (`auth.NormalizeIP` sobre o `RemoteAddr` que o `trustedProxyRealIP` já resolveu): sem normalizar, `::ffff:1.2.3.4` seria um segundo visitante e uma maneira grátis de dobrar todo contador.
- **A supressão alcança a ESCRITA, jamais a resolução.** Um erro aqui quebraria todo link compartilhado da instância para todo visitante, o que é muito pior que um clique contado a mais. Por isso o gate é um booleano que o repositório pergunta sobre uma linha que ele JÁ resolveu, e não existe caminho daqui até a busca do destino.
- **Contexto ausente significa GRAVAR.** `clickctx.Allow` responde `true` sem gate, e o default é carregado: todo chamador está num caminho que sempre escreveu a linha — import, workers, testes. Falhar para "suprimir" faria montar a rota em outro lugar parar de contar em silêncio, e um contador em zero é indistinguível de um dia quieto.
- **Teto de memória obrigatório + sweep.** Sem teto o coalescedor é o novo alvo: o atacante enche o mapa em vez do `click_log`. Encher e recusar inserção o desligaria; encher e recusar gravação zeraria o contador de todo mundo — por isso a pressão evita entradas expiradas primeiro e só então despeja a mais velha de uma amostra limitada, ao custo de no máximo uma linha extra.
- **Todo `INSERT INTO click_log` de produção fica DENTRO de um `if clickctx.Allow(...)`, e um guard de AST é quem diz isso.** Os testes de middleware dirigem resolvers falsos que chamam `clickctx.Allow` eles mesmos: remover a checagem de qualquer um dos dois repositórios deixa todos eles VERDES, e só a suíte de integração pega — que é exatamente o que não roda quando o Docker está ocupado. O guard lê a árvore sintática e não o texto do arquivo, porque um `grep` passaria numa chamada dentro de um comentário ou colocada DEPOIS do INSERT, onde ela não decide nada.
- **Um endereço que não resolve não é um visitante.** `auth.NormalizeIP` devolve `""` para um `RemoteAddr` que não consegue parsear; hashear isso poria todos esses chamadores sob UMA chave, e o primeiro deles suprimiria as linhas de todos os outros. Um coalescedor que perde clique alheio corrompeu justamente o contador que existe para proteger — então sem gate, e essas gravam.
- **`public_click_coalesce_seconds = 0` desliga**, e é a única configuração deste conjunto cujo estado desligado é suportado: o operador está comprando de volta o contador exato, com o `limit_req` do nginx ainda cobrindo a superfície.

O gate viaja pelo CONTEXTO (`internal/pkg/clickctx`) e não por parâmetro porque as duas pontas não se encontram de outro jeito: a identidade do visitante só existe na borda HTTP e o id da entidade só existe no meio da transação do repositório. É o `auditctx` ao contrário — lá um middleware instala um recipiente que o código de baixo anota, aqui instala uma decisão que o código de baixo pergunta.

### INV-183 — Nenhuma entrada controlada pelo cliente compõe uma chave de rate limit.

*Guards:* `TestLoginLimits_TheConfigurableCeilingFitsInsideTheLimiterSet`, e a lente 3 da skill `/abuse-audit`

A pergunta que originou o ADR-47 propunha compor a chave do limitador com `IP + e-mail + User-Agent`. O User-Agent é um cabeçalho que **o cliente escreve**: com ele na chave, o atacante manda um valor diferente por tentativa — dimensão livre e infinita — e ganha um orçamento inteiro por requisição. O balde deixa de existir **enquanto continua parecendo existir**, que é o pior modo de falha desta categoria: a configuração mostra um teto, o log mostra contagens, e nada nunca tranca.

É exatamente o defeito que `server.trustedProxyRealIP` já documenta para o `X-Forwarded-For` (INV-007) — *"an attacker rotates one header value per attempt and never trips a cap"*. Reintroduzi-lo numa chave nova, depois de tê-lo corrigido numa, seria a pior regressão possível.

- **IP e e-mail passam no teste** porque nenhum dos dois é grátis de trocar: o IP custa infraestrutura, e o e-mail é o alvo — trocá-lo abandona a conta que se quer invadir.
- **Registrar ≠ confiar.** O User-Agent continua gravado na trilha (INV-176), onde é útil justamente por não ter autoridade nenhuma: *"mesma conta, dispositivo novo"* é uma pergunta que investigações fazem. A regra proíbe que ele DECIDA, não que ele apareça.
- **Vale para qualquer chave futura**: cabeçalho custom, campo do corpo, cookie, fingerprint de dispositivo. Fingerprinting é esta regra vestida de solução.

### INV-184 — O balde de IP do login conta LARGURA (contas distintas), o de e-mail conta PROFUNDIDADE, e um conjunto cheio TRANCA.

*Guards:* `TestSetMode_RepeatedFailuresOnOneMemberNeverLockTheKey`, `TestLoginFailure_TheIPBucketCountsAccounts_NotAttempts`, `TestLoginFailure_DistinctAccountsFromOneOriginLockIt`, `TestSetMode_MembersAreCappedAndAFullSetIsLockedOut`, `TestSetMode_ReleaseKeepsTheSetTheKeyAlreadyHolds`, `TestLogin_ManyPeopleBehindOneAddressDoNotLockEachOtherOut`, `TestLogin_ASprayAcrossManyAccountsLocksTheOrigin`

Os dois baldes do login respondem perguntas diferentes e contavam a mesma coisa. *"Alguém está martelando ESTA conta?"* é falhas consecutivas por conta e não muda. *"Esta origem está varrendo MUITAS contas?"* passou a ser **contas distintas falhadas por origem** — antes, vinte erros de senha da mesma pessoa e vinte contas sondadas eram indistinguíveis, e atrás de um NAT corporativo a origem é um prédio.

- **Um escritório atrás de um NAT nunca encosta no balde de IP**: cada pessoa é um e-mail com o próprio orçamento. Um spray tropeça na décima conta em vez da vigésima tentativa.
- **Trade-off declarado, e não há perda**: quem martela UMA conta de um só IP deixa de tropeçar aos 20 e passa a ser segurado aos 5 pelo balde de e-mail — parado **antes**, não depois.
- **O e-mail inexistente É membro.** Uma origem sondando quinhentos endereços que não existem é o spray mais puro que existe, e sai de graça porque o endereço desconhecido já cai no mesmo ramo de `CommitFail` que a senha errada (INV-041). O caminho tem de continuar byte-idêntico: `Release` versus contagem não pode virar oráculo de enumeração observável por tempo nem por status.
- **`MaxMembersPerKey` fica ACIMA de todo teto configurável** (128 > 100), e um conjunto cheio TRANCA. Um cap abaixo do teto imporia em silêncio um limite que o dono não escolheu e não consegue ver — a falha do INV-169. Cheio é o único estado em que o limitador perdeu a capacidade de distinguir largura de ruído, e a leitura segura disso é recusar.
- **O conjunto é uma JANELA, não um total corrido.** Cada membro carrega o instante da falha e só conta enquanto está dentro de `LoginWindowMinutes`. Implementado como total corrido, "contas distintas por origem" virava "contas distintas desde o último lockout ou sweep" — um período sem limite superior para uma chave continuamente ativa, então dez pessoas diferentes errando uma vez cada ao longo de uma tarde acumulavam até um teto pensado para dez contas em quinze minutos. É o falso positivo que o §8 do SDD nomeia como critério de reversão.
- **Um SUCESSO não devolve a largura.** `CommitSuccess` zera a contagem escalar e **preserva** o conjunto: uma entrada bem-sucedida não é evidência sobre as outras nove contas que a origem sondou. Apagar a entrada inteira fazia o controle de largura custar UM login para resetar — falhe contra nove contas sob um teto de dez, entre na sua própria, repita para sempre. É o mesmo buraco do `Release`, por outra porta, e foi achado por revisão depois que a mutação achou o primeiro.
- **Todo caminho terminal devolve a reserva.** Ao parar de apagar a entrada, o `CommitSuccess` parou junto de liberar o `inFlight`, e a chave andava uma reserva mais perto de um lockout a cada login bem-sucedido. `TestEveryTerminalPathReleasesTheReservation` nomeia a propriedade para a próxima mudança não a perder em silêncio.
- **A expiração que um commit devolve é a TRANSIÇÃO, não o estado.** `Begin` também grava `lockedUntil` quando recusa pelo teto de in-flight, então toda tentativa concorrente que já tinha reserva lia de volta uma expiração não-zero — e o handler grava uma linha de auditoria por expiração não-zero. Uma rajada de N requisições fazia o servidor inserir ~N linhas permanentes sondando UMA conta: a trilha virava o amplificador que o limitador existe para remover, com o atacante escolhendo o multiplicador.
- **Um sucesso não levanta a penalidade de LARGURA.** `CommitSuccess` zera `lockedUntil` só para chave escalar. Numa chave de conjunto o lockout foi ganho por largura, e limpá-lo ali também reiniciaria o relógio — entregando ao atacante uma forma de encurtar a própria pena com um login a que ele tem direito.
- **O horizonte do sweep tem de ser MAIOR que a janela mais larga configurável** (`memoryRetain()` 2 h contra `MaxLoginWindowMinutes` 60 min), e um teste amarra os dois porque vivem em pacotes diferentes. A entrada não é uma sequência que se reconstrói sozinha: é o conjunto de contas que aquela origem já falhou, e derrubá-la perdoa uma varredura em curso por ela ter pausado.
- **O membro é o identificador SUBMETIDO, não a conta resolvida.** Resolver primeiro fazia o tamanho do conjunto depender de dois identificadores nomearem a mesma conta, e essa diferença é legível de fora: com dez sondas orçadas, um par que colapsou deixa uma de folga e um par que não colapsou não deixa — 429 versus 401 responde "este username pertence a esta caixa?" para um anônimo. A única sonda de username da instância é autenticada de propósito (INV-013).
- **RESIDUAL conhecido, e NÃO fechado por esta mudança:** o balde por CONTA continua chaveado na conta resolvida (`loginBucketKey`), então cinco senhas erradas em `alice` trancam o mesmo balde que `alice@x.com` — e a sexta requisição responde 429 se o username pertence àquela caixa e 401 se não pertence. É a mesma pergunta que o INV-013 mantém autenticada, custa cinco tentativas mais o lockout da vítima por sonda, e é anterior ao ADR-47. Fechá-la exige que o lockout por conta responda como uma falha de credencial (401, com o mesmo piso de duração do INV-041) em vez de 429 — uma mudança no caminho mais sensível do repo, que merece a própria rodada. O balde de ORIGEM não tem esse defeito porque seu membro não é resolvido.
- **`Release` preserva o conjunto.** O `gcLocked` media só `e.fails`, então um `Release` apagava a entrada inteira de uma chave em modo conjunto — e o login libera o slot de IP quando o balde por conta recusa, caminho que o atacante dispara à vontade: martelar uma conta até trancá-la devolveria todo o orçamento de largura da origem. Achado por mutação, não por revisão.

### INV-185 — Os limites de abuso são configuráveis com pisos dos DOIS lados, um valor fora de faixa reverte o CAMPO, e "dinâmico" significa RECARREGAR.

*Guards:* `TestValidateForWrite_RefusesBothDirections`, `TestSanitize_RevertsOneKnobAndKeepsTheRest`, `TestEnforcementFloorsAreNotOne`, `TestCache_FailStaticKeepsTheLastGoodPolicy`, `TestGet_AnOutOfRangeKnobRevertsAloneAndTheRestSurvives`, `TestAbusePolicyAPI_AnAdminReadsButNeverWrites`

O SDD fixou a FORMA de cada controle e deixou a MAGNITUDE aberta, porque magnitude depende da instância: cinco pessoas num servidor doméstico e quarenta atrás de um NAT corporativo não são o mesmo tráfego, e um número compilado estaria errado para uma das duas sem meio de dizer isso.

- **O perigo está no PRODUTO dos pisos, não em cada um.** `{3 contas distintas, 1440 minutos}` era um documento legal, e atrás de qualquer proxy que a instância não avalize aquela origem é todo mundo: três logins errados trancariam todos por um dia. `MaxLoginWindowMinutes` é **60**, que é o valor que o resto deste código já trata como o lockout longo (`bootstrapIP`, `inviteIP`, `pwResetIP`, `oauthIP`). Uma punição maior compra pouco de um limitador em memória que um restart limpa de qualquer jeito, e a ferramenta durável contra um atacante persistente é a lista de bloqueio (ADR-46), que é manual de propósito.
- **Pisos dos dois lados, ao contrário do INV-048.** Um piso de senha só é perigoso quando BAIXO. Um rate limit é perigoso nas duas pontas: alto demais deixa de ser controle e vira observação; baixo demais **VIRA** o ataque — um limite de uma conta por hora entrega a qualquer um que alcance o formulário a capacidade de trancar um escritório inteiro digitando uma senha errada. Por isso `MinLoginDistinctAccountsPerIP = 3`, e não 1.
- **Knobs que AGEM e knobs que RELATAM têm bounds diferentes.** Os três limiares do painel de anomalias não recusam nada: um valor errado dá uma tela ruidosa ou quieta, nunca um usuário trancado. Guardá-los como se fossem a mesma coisa ensinaria que os dois grupos têm o mesmo peso.
- **Fora de faixa reverte o CAMPO, nunca o documento.** É a lição do INV-169 aplicada por construção: no `internal/policy`, um `Validate` que falha devolve o default inteiro, e apertar um limite fez uma instância perder silenciosamente a allowlist do Google junto.
- **Escrita owner-only por permissão TRAVADA** (`instance.rate_limits`), pela razão do INV-178: é a permissão cujo uso errado torna a instância inalcançável para quem a detém. Leitura é de qualquer administrador — quem opera precisa poder VER sob quais regras a instância se defende.
- **"Dinâmico" é recarregar, não se auto-ajustar.** A tela que define os números está na mesma instância que está sendo defendida, e não se pede a um operador que faça deploy no meio de um incidente. O cache falha **ESTÁTICO**: uma falha do banco não decide quantas tentativas uma origem ganha — segue valendo a última política boa, e os defaults compilados antes do primeiro load.
- **UM cache por processo.** Caches separados honrariam a política cada um no seu TTL: a mesma instância rodaria dois regulamentos por até trinta segundos depois de cada save, e o `Invalidate()` do PUT alcançaria só um deles.
- **`app_setting` está fora da superfície de backup nos dois sentidos**, e é isso que impede um zip forjado de reescrever os limites da instância.

### INV-186 — O detector de anomalias RELATA; o bloqueio continua sendo do humano, e o painel não mostra e-mails.

*Guards:* `TestAnomaly_CarriesNoEmailAnywhereInItsJSON`, `TestWiring_TheAnomalyPanelIsMountedUnderAdmin`

O painel ordena origens por sinal — varredura de contas, martelo numa conta, origem já limitada — com a evidência em números e o botão de bloquear pré-preenchido. Ele **nunca dispara sozinho**.

- **Automatizar é entregar a chave da porta a um heurístico cuja entrada o atacante fornece.** O INV-178 já diz qual é o modo de falha deste controle: não é "um bloqueio que não funciona", é uma instância que ninguém alcança, instalada pela pessoa que mais precisava alcançá-la — e um heurístico faria isso sozinho, de madrugada.
- **Só a CONTAGEM de contas distintas, nunca a lista.** O alvo já vive na linha do tempo da trilha; repeti-lo aqui criaria uma segunda superfície de leitura que o INV-175 teve o trabalho de reduzir a uma. O guard falha se QUALQUER campo do JSON contiver `@`.
- **`ip_trusted = false` fica visível.** Numa instalação com proxy, isso significa que o endereço é o do PROXY e a linha é sobre *todo mundo* — esconder a distinção faria o operador bloquear o próprio nginx. É o defeito que originou o SDD inteiro.
- **A recomendação é informada, não automática.** Ao lado de cada knob a tela mostra o que a trilha de fato observou em 30 dias, para o ajuste partir de dado em vez de intuição. Onde não há medida, não renderiza nada; onde a medida existe e vale zero, diz "sem dado" — são duas afirmações diferentes, e inventar um número aqui seria pior que não ter nenhum.
