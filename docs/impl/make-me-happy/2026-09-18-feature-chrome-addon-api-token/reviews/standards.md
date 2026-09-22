# Standards review — feature/chrome-addon-api-token

Run 1: REJECT (README.pt-BR parity breach; CLAUDE.md drift; dead code).
Run 2: REJECT (duplicate pt-BR "Fonte" bullet; stale en layout row) — commit "docs: dedupe pt-BR source bullet and drop the stale en layout row".
Run 3 (final, scoped verification): both items FIXED. README.pt-BR.md:67 single "Fonte:" bullet mirroring README.md:67 "Source:"; layout tables identical without extension/ rows; sole remaining `extension/` match is CLAUDE.md:13 historical prose (pre-ruled acceptable); chrome://extensions occurrences are install-step URLs.

Suppressed (convention holds): a.fx-btn duplication mandated by INV-154; AddonCard inline styles match sibling InvitePanel convention; new comments uniformly why-comments per §7; httperr envelope on new handlers; RejectAPIToken routing preserves the 404-shape (INV-023); i18n parity en/pt/es on all new keys; CI yaml gate correct.

All run-1 items (pt-BR bullets, CLAUDE.md:45/:333, dead wantBuilt/wantVersion fields, admin.addon_step2 key, release.sh trailing space) verified fixed in run 2.

VERDICT: APPROVE
