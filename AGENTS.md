# AGENTS.md

> **This file is a pointer. The authoritative project invariants live in [`CLAUDE.md`](./CLAUDE.md).**

Foldex uses [`CLAUDE.md`](./CLAUDE.md) as the single source of truth for:

- Stack versions (Go, bun, Postgres, MUI, etc.) and bump policy
- Test + coverage requirements (≥85% gate, atomic mode, count=1)
- Documentation update matrix (which doc to touch when behavior changes)
- Data invariants (FK rules, unique constraints, click_log as source of truth)
- UI/UX invariants (Esc closes modals, no native `title=`, density picker, …)
- Definition of Done checklist
- Style choices (Chi router, pgx, slog, plain React + foldex.css)
- Architecture in one paragraph

This `AGENTS.md` exists so Codex / Cursor / Aider and other tools that look
for `AGENTS.md` by convention still find a starting point — but please open
`CLAUDE.md` for the actual rules. Keeping two copies in sync was the source
of drift during early development.

If you're an LLM agent: read [`CLAUDE.md`](./CLAUDE.md) end-to-end before
making any change.
