# N+1 Audit

## 2026-08-16 - Final Audit Sweep (59801b2 + Worktree)

- **HIGH:** none. Entries, preview status, counts and stats use batched/set-based projections; mapped frontend cards start no per-row requests.
- **Recovery follow-up resolved:** preview recovery now sizes each pending projection to scheduled jobs plus currently available queue capacity instead of rereading 1,000 rows per wave. A dropped explicit rerun requests immediate durable recovery.
- **Known bounded findings unchanged:** foreground/background note-media deletion remains MEDIUM/LOW at up to 100 sequential object deletes, and backup export retains one extra stat probe per streamed object. The detailed remediation remains in the 2026-08-14 full-delta section below.

## 2026-08-14 - N1-NEX-004 Change-check Claim Projection

- **Resolved:** `SystemFindDueForCheck` now returns URL, title, interval, prior fingerprint and the claimed `last_checked_at` with each atomically reserved row. `changecheck.Worker` processes that projection directly; the full `SystemGet` query and its per-link `click_log` LATERAL aggregate were removed.
- **Regression lock:** `TestScan_DueBatchDoesNotCallSystemGetPerJob` processes a claimed batch while asserting zero follow-up reads, and the repository integration suite verifies the claimed projection.
- **No new HIGH findings:** the due loop only admits in-memory jobs. One external fetch and one result write are the bounded work for each monitored link, not list enrichment; notification delivery is likewise the admitted payload operation and now runs through a fixed 32-slot queue with at most 8 workers.

## 2026-08-14 - Full Delta Final Audit (effd2ad..174d6ec + Worktree)

- **Scope:** all changed production files from `effd2ad` through `HEAD` (`174d6ec`), all dirty/untracked production files, and direct callers. This covered the shared list planner; entries/links/notes/folders/tags; importer staging; backup snapshot/validation/staged restore/ledger/object listing; storage; note media; preview/screenshots; mail dispatch; authentication; server wiring; and the extracted frontend App, dialog, card, hook, auth, API, and admin components.
- **HIGH:** none. List endpoints issue one data query plus at most one batched tag query, with `listquery.Planner` capping content pages at 500. Import and backup DB application paths preload conflicts, build rows in memory, use `CopyFrom`/set-based statements, and do not issue database calls from item loops. No changed frontend row/card component starts a request; dialog queries are single-entity or shared TanStack Query keys.
- **MEDIUM:** `backend/internal/notemedia/repository.go:239-247` performs one object-store `DeleteObject` per released key on the foreground note update/delete cleanup path (`backend/internal/notes/repository.go:385-391`). It is bounded by `DeleteBatchMax = 100` (`repository.go:15-17`, sliced at `:211-213`) and a 10-second caller timeout, but remains up to 100 sequential storage round trips. A future change can expose explicit multi-delete on the narrow deleter port while preserving the row-lock/ownership transaction.
- **LOW:** `backend/internal/notemedia/repository_system.go:46-54` performs the same per-key object delete in the 15-minute background sweep. Each run is capped at 100 by `DeleteBatchMax` and `LIMIT`, so it cannot become an unbounded hot-path fan-out.
- **LOW:** backup export streams one object at a time at `backend/internal/backup/service.go:173-178`; `OpenObject` at `backend/internal/storage/client.go:200-210` performs a per-object stat probe before the payload stream. Export is an explicit maintenance operation bounded by 99,998 file entries, 64 MiB per file, and 4 GiB expanded total (`backend/internal/backup/archive.go:14-20`). One payload GET per selected object is intrinsic; the extra preflight remains a bounded round-trip cost.
- **LOW:** backup restore builds one PUT task per selected archive object at `backend/internal/backup/service.go:750-790`. Work is bounded by the same 99,998-entry/4-GiB archive limits and executes through a cancellation-aware pool capped at 8 (`service.go:825-850`). This is required payload transfer rather than a database lookup follow-up.
- **Other bounded round-trip loops, not N+1 findings:** link/note create collision retries are for one row and cap at 100 (`backend/internal/pkg/slug/create.go:11,37-60`); folder serialization retries cap at 3; auth cleanup executes a fixed three statements; legacy link-image cleanup executes a fixed four deletes; RustFS startup polls one endpoint until its context deadline; mail delivery has 2 workers and 32 process-wide queue slots, with one send per admitted message.
- **Batched/fixed object costs:** export performs exactly three namespace LIST streams; skip restore resolves exact keys with at most three namespace LIST streams; wipe uses S3 multi-delete (up to 1,000 keys per request); restore performs one PUT only for each missing payload. Preview recovery lists at most 1,000 IDs and only enqueues them in memory; each worker's DB/fetch work is the preview job itself, not a per-row enrichment query.
- **Frontend result:** extracted `LinkCard` parts/interactions only update query-cache data and invoke user-triggered mutations; the note dialog fetches at most its one open note and reuses shared `['tags']`/`['folders']` queries. Admin/user/invite arrays and all changed card/tag/folder maps render existing payloads without per-row requests.

## 2026-08-14 - N1-NEX-002 Backup Restore Staging

- **Resolved:** skip/wipe/duplicate no longer query or insert per tag/folder/link/note or per slug candidate. Restore preloads owner URL/tag conflicts and bounded relevant global slug collisions, reserves slugs in memory, stages rows with `CopyFrom`, then inserts entities/associations/clicks with fixed set-based statements.
- **Regression lock:** pgx tracing counts both queries and `CopyFrom`; 48 tags + folders + links + notes plus 96 associations and 96 clicks take 33 operations in skip and 29 in duplicate, both under a fixed 40-operation budget. The fail-once suite separately proves the durable skip ledger prevents all DB mutation on a completed repeat.
- **Object I/O bound:** an incomplete skip resolves all exact destination keys with one bulk interface call, implemented as at most three paginated namespace LISTs, then uploads only missing objects through a cancellation-aware pool capped at 8. Wipe submits only owner-scoped exact keys through S3 multi-delete instead of one DELETE per key. Completed repeats do zero LIST/PUT operations. Remaining work is one PUT per missing object, one GET per exported object, paginated LIST pages, and one multi-delete request per S3 batch (up to 1000 keys).
- **No open N+1 findings:** remaining loops build staging/mapping slices, stream ZIP entries, or run the bounded object work itself; no list loop issues an unbounded sequence of database round trips.
- **LEAK-HYD-006 acceptance recheck:** export uses a fixed set of row cursors plus one owner-ID `ANY` query, three namespace LIST streams, and one required GET per selected object. Callback filtering adds no per-object metadata request; note-media preparation is local decode/encode work and publication retains the existing bounded PUT pool. No HIGH/MEDIUM/LOW N+1 was introduced.

## 2026-08-13 - N1-NEX-001 Import Staging

- **Resolved:** `backend/internal/importer` no longer performs tag/folder misses, slug-candidate checks, wipe cleanup, link inserts, click backfill, or tag attachment per imported row. JSON/Netscape apply now uses bounded owner-URL/global-slug preloads, in-memory slug reservation, temporary staging tables populated with `CopyFrom`, and fixed set-based statements.
- **Regression lock:** pgx query/CopyFrom tracing records 14 database calls for both 4-row and 200-row skip imports, and 15 for both wipe sizes.
- **No open N+1 findings:** item loops only normalize/build staging data in memory. The one remaining per-insert operation is the required post-commit in-process `worker.Enqueue(id)`; it performs no database/network round trip, and failed queue admission is recovered from durable `preview_status='pending'`.
- **Amplification bound:** JSON validation and the apply boundary enforce 10,000 synthetic clicks per link and 1,000,000 cumulatively per request.

## 2026-08-12 - Authentication Atomicity

- **Resolved:** `backend/internal/auth/repository_2fa.go:replaceRecoveryCodesTx` previously issued one insert per recovery-code digest. The fixed-size set is now inserted with one `unnest($2::bytea[])` statement.
- **No open findings:** the other loops in the changed authentication paths compare bounded in-memory values, generate digests without I/O, or execute a fixed three-query maintenance sweep. No list path performs per-item database or network work.

## 2026-08-12 - Screenshot Egress Hardening

- **No open findings:** the changed paths perform one DNS lookup and at most one connection attempt per resolved address for a single page request. Prefix checks are bounded in-memory scans over the fixed IANA registry; no list, repository, or frontend row performs per-item database or network work.
- **Resolved recovery path:** the pending sweep now enqueues IDs directly without a per-ID `SystemGet`; workers use a narrow four-column projection instead of the full link query with `click_log` LATERAL aggregation. Migration `000026` adds a partial pending index. The remaining per-job reads are the work itself, not list-follow-up I/O.
- **No open findings:** operation-owned object cleanup is one bounded request per superseded capture, and legacy cleanup is a fixed extension set rather than list-sized I/O.
- **No open findings after manual-upload hardening:** upload/publish/cleanup remains a fixed sequence for one link; the four legacy deletes iterate a constant extension allowlist, not user-sized data. CDP request accounting observes browser traffic without adding DB or network calls.
