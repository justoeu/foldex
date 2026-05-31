# AGENTS.md

> **Project invariants live in [`CLAUDE.md`](./CLAUDE.md).** Read it before making any change.

This file holds the **canonical prompts for the three post-implementation review agents** required by CLAUDE.md §6 (Definition of Done) and §9 (Post-implementation agent sweep). Spawn all three in parallel via the `Agent` tool — never skip and never serialize.

---

## When to run

After every implementation task, once typecheck + tests + coverage pass, **before** declaring done and **before** opening a PR. Mandatory — no exceptions for "small changes."

> **Pre-push gate (CLAUDE.md §6.1) is the hard prerequisite.** Run the EXACT commands the CI workflow runs (not the ones it used to run) and confirm they're green LOCALLY before any `git commit`, `git push`, or `gh pr create`. Pushing relying on "the CI will catch it" wastes minutes per round-trip AND consumes GitHub Actions billing for a check that should have happened in 30s locally. If the change touches `.github/workflows/*.yml`, grep `^\s+run:` from the new YAML and execute every line locally before pushing.

## How to run

In a single tool-use block, issue three `Agent` calls (`run_in_background: true`) with `subagent_type: general-purpose`. The harness notifies as each one finishes; do not poll or sleep.

For each prompt below, replace the `<SESSION SCOPE>` placeholder with the actual artifacts the agent should focus on — typically the commit hashes added this session, plus the branch they live on. Be specific: list the commit SHAs and the file globs that changed.

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
- Test quality / coverage — owned by the Test Quality agent
- Security (XSS, secret leak, injection, supply chain) — owned by the Security agent

What YOU review:
1. **Architectural coherence** — do the changes violate any CLAUDE.md §4 or §5 invariant?
2. **Code quality** — naming, organization, complexity, dead code, comments that violate the
   "no what comments, no task references" rule in §7.
3. **React idiomaticity** (frontend) — hooks correctness, missing cleanup, prop design, unnecessary
   re-renders, memoization where it actually pays off.
4. **Backend idiomaticity** (when applicable) — Chi handlers, pgx tx scoping, slog usage, error
   envelope per §7.
5. **CI / workflow correctness** (when applicable) — matrix shape, cache scoping, race conditions
   between jobs, missing permissions.
6. **Decisions that could be better** — inconsistent naming, real (not premature) simplifications.

Reporting format:
- **Blockers** (must fix before merge): bullet — `file:line` — why.
- **Non-blockers** (suggestions / follow-ups): same shape.
- **Good things** (1-2 highlights, optional): so the author knows what to keep.

Hard cap: 400 words. Focus on what matters.
```

---

## Agent 2 — Test Quality

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
- General code quality — owned by the Code Review agent
- Security risks — owned by the Security agent

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

## Agent 3 — Security Review

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
- General code quality — owned by the Code Review agent
- Test coverage — owned by the Test Quality agent

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
4. Only declare the implementation done once the three reports are visible and every HIGH is resolved.

The three agents are split by concern on purpose. **Never merge them into one** ("the change is small") — the sweep is precisely the safety net for changes that look small.
