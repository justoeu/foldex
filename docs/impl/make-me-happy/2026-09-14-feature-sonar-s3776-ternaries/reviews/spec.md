# Spec review — `feature/sonar-s3776-ternaries`

Spec: `docs/SDD-SONAR-S3776-TERNARIES.md`

## (a) Missing / partial

**None vs the leftover REJECT list.** Flatten helpers: `imageDropHint`/`imageSubmitLabel`, `importSubmitLabel`, `anomalyBlockAction`, `NoteViewBody`. Scanner: true-arm stack + single-line false-arm regex + fixture. Production glob excludes tests.

Go: `gocognit -over 15` empty under `backend/internal` production. `docs/TASKS.md` 2026-09-15 S3776 row present.

**Coverage ≥ 95% of touched product code** is not met (`coverage.json` 85.9%). That is the MMH pack gate; correctness already REJECT. No missing flatten/extract vs this spec pass.

## (b) Scope creep

**None.** Helpers package-local; no new lib, NOSONAR, native `<dialog>`, i18n/CSS, or README.

## (c) Wrong vs spec

**None** on leftover sites. Remaining `? :` are single-level.

VERDICT: APPROVE
