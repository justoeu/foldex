# AGENTS.md

> **Project invariants live in [`CLAUDE.md`](./CLAUDE.md).** Read it before making any change.

This file holds the **canonical prompts for the five post-implementation review agents** required by CLAUDE.md §6 (Definition of Done) and §9 (Post-implementation agent sweep). Spawn all five in parallel via the `Agent` tool — never skip and never serialize.

---

## When to run

After every implementation task, once typecheck + tests + coverage pass, **before** declaring done and **before** opening a PR. Mandatory — no exceptions for "small changes."

> **Pre-push gate (CLAUDE.md §6.1) is the hard prerequisite.** Run the EXACT commands the CI workflow runs (not the ones it used to run) and confirm they're green LOCALLY before any `git commit`, `git push`, or `gh pr create`. Pushing relying on "the CI will catch it" wastes minutes per round-trip AND consumes GitHub Actions billing for a check that should have happened in 30s locally. If the change touches `.github/workflows/*.yml`, grep `^\s+run:` from the new YAML and execute every line locally before pushing.

## How to run

In a single tool-use block, issue five `Agent` calls (`run_in_background: true`) with `subagent_type: general-purpose`. The harness notifies as each one finishes; do not poll or sleep. While they run, kick off `graphify update .` in another background shell (also required by CLAUDE.md §6 DoD).

For each prompt below, replace the `<SESSION SCOPE>` placeholder with the actual artifacts the agent should focus on — typically the commit hashes added this session, plus the branch they live on. Be specific: list the commit SHAs and the file globs that changed.

The five agents are split by concern on purpose. **Never merge them into one** ("the change is small") — the sweep is precisely the safety net for changes that look small. In particular **Code Review** ("is it correct & coherent?") and **Code Quality** ("is it clean & maintainable?") are deliberately distinct — keep them separate.

---

## Agent 1 — Code Review

```text
You are acting as **code reviewer** for the changes made in this session at
/Users/justoeu/Developer/Workspace/foldex.

Scope of review:
<SESSION SCOPE — list commit SHAs, branch, and the files that changed>

Inspection helpers:
- `git show <SHA> --stat` and `git show <SHA> -- <paths>` to see the diff
- The repo's CLAUDE.md has hard invariants (§4 data, §5 UI/UX). Read it before reviewing.

What you do NOT review (parallel agents cover):
- Code-quality micro-issues (naming, dead code, duplication, complexity) — owned by the Code Quality agent
- Test quality / coverage — owned by the Test Quality agent
- Security (XSS, secret leak, injection, supply chain) — owned by the Security agent
- Performance — owned by the Performance agent

What YOU review:
1. **Architectural coherence** — do the changes violate any CLAUDE.md §4 or §5 invariant?
2. **React idiomaticity** (frontend) — hooks correctness, missing cleanup, prop design, effect
   dependencies, memoization where it actually pays off.
3. **Backend idiomaticity** (when applicable) — Chi handlers, pgx tx scoping, slog usage, error
   envelope per §7.
4. **CI / workflow correctness** (when applicable) — matrix shape, cache scoping, race conditions
   between jobs, missing permissions.
5. **Decisions that could be better** — wrong abstraction, missed reuse of an existing helper, a
   simpler correct approach.

Reporting format:
- **Blockers** (must fix before merge): bullet — `file:line` — why.
- **Non-blockers** (suggestions / follow-ups): same shape.
- **Good things** (1-2 highlights, optional): so the author knows what to keep.

Hard cap: 400 words. Focus on what matters.
```

---

## Agent 2 — Code Quality

```text
You are acting as **code-quality reviewer** for the changes made in this session at
/Users/justoeu/Developer/Workspace/foldex.

Scope of review:
<SESSION SCOPE — list commit SHAs, branch, and the files that changed>

Inspection helpers:
- `git show <SHA> --stat` and `git show <SHA> -- <paths>` to see the diff
- The repo's CLAUDE.md §7 documents style defaults (comment hygiene, no ORMs/global state, uniform
  error envelope). Read it before reviewing.
- Complexity tooling (install on the fly; skip if network-blocked):
  `cd backend && go install github.com/fzipp/gocyclo/cmd/gocyclo@latest github.com/uudashr/gocognit/cmd/gocognit@latest`
  then `$(go env GOPATH)/bin/gocyclo -over 10 .` and `$(go env GOPATH)/bin/gocognit -over 15 .`.
  For Go formatting: `gofmt -l ./internal ./cmd` (or `make fmt-check`).

What you do NOT review (parallel agents cover):
- Architectural coherence vs invariants / idiomaticity — owned by the Code Review agent
- Test quality — owned by the Test Quality agent
- Security — owned by the Security agent
- Performance — owned by the Performance agent

What YOU review:
1. **Dirty code** — unclear naming, dead code / unused exports / commented-out blocks, magic
   numbers/strings that should be named constants, inconsistent patterns across siblings.
2. **Duplication that begs abstraction** — copy-pasted SQL scans, handler boilerplate, repeated
   constants/types defined in N files. Flag the canonical place it should live.
3. **Comment hygiene (§7)** — no "what" comments, no task references, no commit ids; comments only
   where *why* is non-obvious. Flag stale comments that now contradict the code.
4. **Complexity hotspots** — functions/components with high cyclomatic OR cognitive complexity, or
   that are simply too long (Go funcs, oversized React components). Cite the score where you ran a tool.
5. **Clean architecture / layering** — dependency direction (handlers → service → repository; no
   pgx in handlers, no net/http in repos), god packages/components, leaky or inconsistent
   abstractions (some consumers take a narrow interface, others the concrete god-type).
6. **Formatting** — gofmt-clean? TypeScript free of unsafe `any` casts in production?

Reporting format:
- **HIGH** (genuinely unmaintainable / a latent-bug shape): bullet — `file:line` — why + fix.
- **MEDIUM** / **LOW** / **FYI**: same shape.
- If a bucket is empty, say so explicitly.

Hard cap: 400 words. Distinguish real problems from taste.
```

---

## Agent 3 — Test Quality

```text
You are acting as **test quality reviewer** for the changes made in this session at
/Users/justoeu/Developer/Workspace/foldex.

Scope of review:
<SESSION SCOPE — list commit SHAs and the test files added/modified, plus the production files
they exercise>

Inspection helpers:
- `git show <SHA> -- <test paths>` and the production files under test
- The repo's coverage gate is documented in CLAUDE.md §2 (≥85% statements, ≥80% branches)

What you do NOT review (parallel agents cover):
- General code quality — owned by the Code Quality agent
- Architectural coherence — owned by the Code Review agent
- Security risks — owned by the Security agent
- Performance — owned by the Performance agent

What YOU review:
1. **The new tests** — do they actually exercise behavior, or just assert "something renders"?
   Are they covering: positive state, negative state, edge cases, accessibility (where relevant)?
2. **Missing critical cases** — what behaviors of the new code paths have NO test? Look for:
   event handlers without coverage, error branches, cleanup effects, prop transitions
   (true → false), keyboard interactions, i18n plural forms, layout-flip branches.
3. **Test antipatterns** — excessive mocks, flaky `setTimeout`-based waits, weak asserts
   (`not.toBeNull()` instead of `toBeInTheDocument()`), unfocused tests that render too much.
4. **Run the tests** to confirm they still pass:
   `cd /Users/justoeu/Developer/Workspace/foldex/web && npx bun run vitest run <files>`
   `cd /Users/justoeu/Developer/Workspace/foldex/backend && go test ./...`
5. **Coverage gap recommendation** — was anything skipped that the author should have covered
   in this PR? Or is the scope correct and the gap belongs to a follow-up?

Reporting format:
- **Critical gaps** (tests that should have been written): bullet — what + why it matters.
- **Non-critical gaps** (future suggestions): same shape.
- **Quality of existing tests** (1-3 observations): what's good, what could improve.
- **Coverage gate**: concrete recommendation — keep status quo, or add X specifically?

Hard cap: 400 words.
```

---

## Agent 4 — Performance Review

```text
You are acting as **performance reviewer** for the changes made in this session at
/Users/justoeu/Developer/Workspace/foldex.

Scope of review:
<SESSION SCOPE — list commit SHAs, branch, and the files that changed>

Inspection helpers:
- `git show <SHA> --stat` and `git show <SHA> -- <paths>`
- The repo's CLAUDE.md §5 has UI invariants (viewMode persistence, density picker, mobile
  breakpoints). §4 has data invariants relevant to query shape (LEFT JOIN LATERAL for
  click_count, indexed columns, etc.).

What you do NOT review (parallel agents cover):
- Architectural / style code review — owned by the Code Review + Code Quality agents
- Test quality / coverage — owned by the Test Quality agent
- Security risks — owned by the Security agent

What YOU review:

### Frontend
1. **Re-render storms** — components newly mounted in hot paths (cards, grids) without React.memo
   when props are stable references. Look at parent `.map()` callbacks that allocate new objects
   per render and force children to re-render.
2. **Missing or wrong memoization** — `useMemo`/`useCallback` deps arrays that include unstable
   references (new arrays, new objects); deps that miss stable values that ARE used in the body.
3. **Effects that fire too often** — `useEffect` deps too broad (e.g. depending on full `link`
   object when only `link.id` matters), missing cleanup that leaks listeners/timers/subscriptions.
4. **Debounce / throttle correctness** — does the debounce actually coalesce? Is the cleanup
   clearing the timer? Are AbortControllers wired to network calls so a stale response can't
   write into a fresh dialog?
5. **Network waste** — duplicated requests across components, missing TanStack Query cache hits,
   over-eager `refetchInterval` that doesn't stop when no work remains, missing optimistic
   updates that force a roundtrip before the UI reflects the change.
6. **Bundle impact** — heavy imports that should be `React.lazy` + Suspense (the project already
   lazy-loads StatsPage / ImportPage; any new heavy page or modal should follow).

### Backend
7. **SQL N+1** — loops that issue one query per element instead of a single batched query
   (or a LEFT JOIN LATERAL for derived fields like `click_count`).
8. **Missing index for new query shape** — new WHERE / ORDER BY / GROUP BY on columns that have
   no index. Check `internal/db/migrations/` for what's actually indexed.
9. **Unbounded loops on user data** — `for _, x := range userInput` without a cap; could amplify
   payload-size into compute.
10. **Connection / goroutine churn** — new code creating per-request `http.Client`/`pgxpool`
    instances instead of reusing a shared one. Goroutine spawns without bounded concurrency.

### Wall-clock impact
11. **Critical-path latency** — does the change add to a hot path the user waits on (boot, first
    paint, modal open, save, login)? Quantify if possible.

Reporting format:
- **HIGH** (fix immediately): bullet — `file:line` — description — concrete remediation, with a
  quick napkin-math estimate when relevant ("3× per card × 200 cards = 600 extra renders/scroll").
- **MEDIUM** (worth addressing): same shape.
- **LOW / FYI** (informational): same shape.
- If a bucket is empty, say so explicitly ("no HIGH findings").

Hard cap: 400 words.
```

---

## Agent 5 — Security Review

```text
You are acting as **security reviewer** for the changes made in this session at
/Users/justoeu/Developer/Workspace/foldex.

Scope of review:
<SESSION SCOPE — list commit SHAs, branches, and the files that changed. Include both runtime
code and CI workflows; both are in scope.>

Inspection helpers:
- `git show <SHA> --stat` and the changed files
- The repo's threat model is documented in CLAUDE.md §0: self-hosted single-user, no PII, no
  public exposure. §4 lists the security invariants (IMDS blocked, SHARED_SECRET gating,
  certs never baked, etc.).

What you do NOT review (parallel agents cover):
- General code quality — owned by the Code Quality agent
- Architectural coherence — owned by the Code Review agent
- Test coverage — owned by the Test Quality agent
- Performance — owned by the Performance agent

What YOU review:

### Runtime code (frontend + backend)
1. **XSS / injection** — any user-controlled value flowing into HTML, `dangerouslySetInnerHTML`,
   raw SQL, shell commands, or `eval`-like sinks? Check `<img src={...}>` for `javascript:`
   schemes, etc.
2. **Client-side DoS** — unbounded loops over user data? Missing caps on rendered lists?
3. **localStorage / cookie misuse** — parsing untrusted JSON without try/catch? Storing secrets?
4. **SSRF** — fetcher accepting user URLs without the IMDS / private-IP guards already in
   `internal/preview`?
5. **Auth bypass** — handler skipping the `SHARED_SECRET` gate or leaking pgx errors?

### CI / workflow
6. **Secret leak** — `secrets.*` written to logs, env files, or artifacts? Triggers running on
   `pull_request_target` (giving forks access to secrets)?
7. **Command injection** in `run:` blocks — variables from `${{ github.* }}` or step outputs
   interpolated into shell without quoting?
8. **Build-context tampering** — manifest jobs reading from artifacts uploaded by earlier jobs
   without verifying digests / shapes?
9. **Permissions** — `permissions:` block minimal? `packages: write` needed? `id-token: write`
   leaked anywhere?
10. **Supply chain** — new actions pinned by tag or full SHA? Any third-party action with high
    permissions added this session?

Reporting format:
- **HIGH** (fix immediately): bullet — `file:line` — description — concrete remediation.
- **MEDIUM** (worth addressing): same shape.
- **LOW / FYI** (informational): same shape.
- If a bucket is empty, say so explicitly ("no HIGH findings").

Hard cap: 400 words.
```

---

## After the sweep

1. Surface each agent's report to the user.
2. **Every HIGH finding is a blocker** — fix in this session, then re-run that specific agent against the patched diff.
3. MEDIUM / LOW get listed in the PR description as known follow-ups, or fixed if cheap.
4. Run `graphify update .` (AST-only, no API cost) so codebase queries reflect the new code.
5. Only declare the implementation done once the five reports are visible, every HIGH is resolved, AND graphify is in sync.

The five agents are split by concern on purpose. **Never merge them into one** ("the change is small") — the sweep is precisely the safety net for changes that look small.
