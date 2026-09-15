# Lower production cognitive complexity (go:S3776) and nested ternaries (typescript:S3358)

Kind: refactor

Goal:
Clear Sonar `go:S3776` and `typescript:S3358` on production Foldex code by extracting helpers, using early returns, and replacing nested conditional expressions with if/else or closed lookup tables. Behavior, payloads, and UI contracts stay byte-equivalent.

Actors:
Maintainers and CI/Sonar. No user-facing actor. Operators and signed-in users must see the same screens, status codes, and JSON envelopes as before.

In scope:
- Production Go under `backend/internal/**` whose cognitive complexity is **> 15** as measured by `gocognit` (Sonar `go:S3776` default). Inventory at pack start: **63 functions** (auth 18, backup 9, backupagent 8, folders 4, plus preview/mailer/changecheck/screenshot/importer/…). Worst: `CompleteEmailFactorEnrollment` 25, `acquireBrowser`/`preview.Worker.process`/`DrillJob.Run` 24, `AuditStatsSince`/`tryStepUpProof`/`folders.List`/`smtpMailer.Send`/`UpdateUser` 23.
- Production TypeScript/TSX under `web/src/**` with **nested ternary expressions** (`cond ? a : cond2 ? b : c` and deeper), including but not limited to: `AvailabilityHint`, `FolderDialog` (kicker/title), `FolderPicker`, `ConflictModePicker`, `BackupRestoreDialog`, `BackupSection`, `BackupScheduleEditor`, `AuditSignals`, `UsernameRow`, `SectionCard`, `EmailRow`, `StatsPage`.
- Tests added or extended for every extracted helper / flattened branch that currently has no direct coverage. Red → green per task.
- Docs: this SDD plus a `docs/TASKS.md` log row when the pack ships. No README/user-facing copy unless a string actually changes (it must not).

Out of scope:
- Test-only files (`*_test.go`, `web/src/**/*.test.*`, `web/src/test/**`).
- `backend/cmd/**` boot helpers (e.g. `cmd/rustfs-bootstrap`).
- Native `<dialog>` / `<fieldset>` (`typescript:S6819`) — clashes with INV-121/156.
- CSS contrast (`S7924`), React index keys (`S6479`), other Sonar rules.
- New features, schema, API routes, i18n keys, CSS classes, or dependency bumps.
- Version bump (stays on `main` after Quality Gate of #132).
- Merging this pack into `main` from a worktree. Target branch is `feature/sonar-s3776-ternaries` only.

Contracts:
None new. Existing JSON envelopes, HTTP status mapping, `/api/*` shapes, and rendered copy stay identical. Extracted helpers are unexported unless a second package already needs them; prefer package-local functions over a new `pkg/` type.

Invariants:
- CLAUDE.md §4 and §5 (INV-001…187, UI INV-120…168) — especially owner-scoped queries, CSRF, 2FA spend-vs-verify, folder password split, note HTML sanitization, SSRF dialer, dialog Esc, overlay portals.
- Layering: handlers do not grow a service type for single-repo CRUD; extract a helper, do not invent a layer.
- Errors stay in `{ "error": { "code", "message" } }`; repositories remain HTTP-free.
- No `NOSONAR` / complexity-suppression comments as the fix.
- Flattening method: closed enum/state → lookup table; otherwise early-return if/else. No new libraries. No nested ternary left in the touched production file.
- Go gate after each function: `gocognit` of that function ≤ 15. TS gate: the nested `? : ?` expression is gone.

Done when:
- Every in-scope Go function measures ≤ 15 (`gocognit -over 15` on `backend/internal` excluding `*_test.go` is empty).
- Every in-scope production nested ternary is gone (no `typescript:S3358` on `web/src` production files).
- Baseline tests on `feature/sonar-s3776-ternaries` green before the first RED; each task has a failing test first.
- Coverage of **touched product code** ≥ 95% (MMH gate) and repo gates still hold (≥85% statements / ≥80% branches frontend; backend `./...`).
- Immutability: existing tests that encoded the old control flow still pass without weakening asserts.
- Three worktrees merged into `feature/sonar-s3776-ternaries`, never into `main`.
