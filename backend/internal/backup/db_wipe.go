package backup

import (
	"context"

	"github.com/jackc/pgx/v5"
)

// ────────────────────────────────────────────────────────────────────────────
// Wipe mode.

func wipeAll(ctx context.Context, tx pgx.Tx) (Counts, error) {
	var c Counts
	// Count what we're about to delete (for the report). click_log/link_tag
	// are polymorphic — these counts span both link and note rows, matching
	// the combined LinkTags/ClickLogs fields restoreIdentity reports back.
	if err := tx.QueryRow(ctx, `SELECT count(*) FROM click_log`).Scan(&c.ClickLogs); err != nil {
		return c, err
	}
	if err := tx.QueryRow(ctx, `SELECT count(*) FROM link_tag`).Scan(&c.LinkTags); err != nil {
		return c, err
	}
	if err := tx.QueryRow(ctx, `SELECT count(*) FROM link`).Scan(&c.Links); err != nil {
		return c, err
	}
	if err := tx.QueryRow(ctx, `SELECT count(*) FROM note`).Scan(&c.Notes); err != nil {
		return c, err
	}
	if err := tx.QueryRow(ctx, `SELECT count(*) FROM folder`).Scan(&c.Folders); err != nil {
		return c, err
	}
	if err := tx.QueryRow(ctx, `SELECT count(*) FROM tag`).Scan(&c.Tags); err != nil {
		return c, err
	}
	// TRUNCATE order respects FKs through CASCADE. `note` has no FK CASCADE
	// dependents of its own (link_tag/click_log lost their FK to link/note in
	// migration 000014 — cascade is app-level elsewhere, but a blanket TRUNCATE
	// here doesn't need it since every listed table is wiped together).
	if _, err := tx.Exec(ctx, `TRUNCATE TABLE click_log, link_tag, note, link, folder, tag RESTART IDENTITY CASCADE`); err != nil {
		return c, err
	}
	// app_setting is a standalone KV table (no FK edges), wiped separately so a
	// wipe restores to EXACTLY the snapshot's settings — including "no master
	// password" when the snapshot predates ADR-29.
	if _, err := tx.Exec(ctx, `TRUNCATE TABLE app_setting`); err != nil {
		return c, err
	}
	return c, nil
}

// restoreIdentity inserts everything from snap with the original IDs
// preserved. After all INSERTs, advances each sequence to max(id)+1 so future
// auto-IDs don't collide.
//
// All five loops use pgx.CopyFrom (PostgreSQL COPY protocol) instead of
// per-row INSERTs. The wipe path handles the worst-case restore volume
// (a power-user backup of hundreds of thousands of click_logs); per-row
// INSERTs amortized to one network round-trip per row turned a 1M-row
// click_log restore into 1M sequential INSERTs. CopyFrom batches them in a
// single streaming upload — typically 10-50× fewer round-trips. CopyFrom
// is safe here because wipe mode already TRUNCATEd, so there are no
// conflicts to handle and no RETURNING values to capture.
