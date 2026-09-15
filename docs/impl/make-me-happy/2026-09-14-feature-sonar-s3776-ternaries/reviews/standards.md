# Standards — `feature/sonar-s3776-ternaries`

Scope: vs `origin/main`. Sources: CLAUDE.md §4/§5/§7, AGENTS.md, SDD-SONAR-S3776-TERNARIES.md, Fowler ch.3.

## (a) Documented standards

**No hard breach.**

- **No NOSONAR as the fix**: production Go/TS has no `NOSONAR`. The string appears only in `gocognit_test.go` failure messages.
- **No new libraries**: `backend/go.mod` / `web/package.json` unchanged. Cognitive gate is a test-only copy of gocognit (`cognit_ast_test.go`).
- **No native `<dialog>`**; flatten keeps existing modal markup (INV-121/156).
- **Layering**: extracts are package-local unexported helpers. No new CRUD service type.
- **Repos stay HTTP-free**: `httperr` only in handler-side helpers.
- INV-001 (`listWhere` still `f.user_id = $1`), INV-003/011/027 (spend vs verify), INV-019 (`userColumns`), INV-040 (Serializable rotate), INV-143 (ConflictModePicker dual encoding) held.

## (b) Fowler smells (judgement)

- **Shotgun Surgery** across many packages — the spec’s point; not a reject.
- **Duplicated Code**: `cognitVisitor` copied into many `*_test.go` files. Allowed: SDD forbids a new library.
- **Middle Man** (tiny): one-branch wraps as the cost of ≤15.

VERDICT: APPROVE
