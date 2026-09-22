# Correctness review — feature/chrome-addon-api-token

Run 1: REJECT (HIGH stale dist embed — zip carried pre-standalone README; MEDIUM release.sh rollback missed dist restore; LOW rotate ghost-row invalidation; LOW axisSteps zero-viewport infinite loop).
Run 2 (final, after foldex da2cfae + foldex-addon bb6e3c0):

1. Stale embed — FIXED: `make extension` rebuild matches HEAD digest; git status clean.
2. release.sh rollback — FIXED: trap restores dist after mid-bump rewrite; ordering verified (BUMP_STARTED before rewrites; import before add/commit). FYI accepted: `git checkout -- dist` restores from index, so a post-add/pre-commit hook rejection leaves staged residue (visible, not silent).
3. Rotate ghost row — FIXED: onError invalidates ['api-tokens'] with why-comment; suite 12/12.
4. axisSteps — FIXED: `!(viewport > 0)` guard → degenerate [0] step + test; addon suite 58/58.
5. Vitest spy-teardown RangeError noise — accepted as not-ours; suite green.

Regression: go test ./internal/addon/ ./internal/auth/ green. Verified sound additionally: FOR NO KEY UPDATE serialization at cap=1, version string regex before headers (no injection), HEAD no-body + Content-Length, mount order 404-shape (mutation-tested), zero-byte embed → 503, multipart upload shape, legacy-key migration, alarm lifecycle, link→image ordering with fallback.

VERDICT: APPROVE
