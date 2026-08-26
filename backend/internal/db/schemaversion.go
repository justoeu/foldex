package db

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// RequiredSchemaVersion is the newest migration this binary's queries depend on.
//
// Bump it whenever a migration adds something the Go code READS or WRITES —
// not merely when a migration lands. It is the number that turns "your database
// is older than your binary" into a boot failure instead of a 500 hours later.
//
// Migration 000038 deliberately does NOT bump it: it adds an INDEX, and no
// query depends on an index existing — the same SQL returns the same rows
// without it, only slower. Bumping here would refuse the boot of every
// instance that has not run the migration yet, trading a real outage for a
// planner improvement.
//
// 41 was ADR-43 PR5: the BACKEND started reading backup_run (000040) and
// expecting role_permission's instance.backup seed (000041) — before PR5 only
// the agent touched that table, which is why 000040 itself did not bump this.
// 42 is ADR-44: the backend reads and writes backup_schedule and reads
// backup_agent_state (the agenda surface). Distinct from
// backupagent.RequiredSchemaVersion — also 42 now, but tracked separately
// because the two binaries' dependencies move independently.
const RequiredSchemaVersion = 42

// ErrSchemaOutdated is returned when the database has not been migrated.
var ErrSchemaOutdated = errors.New("db: schema is older than this binary requires")

// CheckSchemaVersion refuses to let the process continue against a database
// that has not been migrated.
//
// Migrations in this project are applied deliberately (`make migrate-up`), not
// on boot — a schema change on a container restart is not something an operator
// should discover after the fact. The cost of that choice is this check.
//
// Without it the failure is both delayed and irreversible: with AUTH_ENABLED on,
// an operator who upgrades the image without migrating still completes the setup
// screen successfully, because bootstrap issues a session directly and never
// touches the new columns. That consumes the ONE-SHOT setup step. The break
// arrives on their next sign-in, as a generic 500 from a query naming a column
// that does not exist — with no way back to the setup screen and nothing on
// screen saying "run the migrations".
func CheckSchemaVersion(ctx context.Context, pool *pgxpool.Pool) error {
	var version int64
	var dirty bool
	err := pool.QueryRow(ctx,
		`SELECT version, dirty FROM schema_migrations LIMIT 1`).Scan(&version, &dirty)

	if errors.Is(err, pgx.ErrNoRows) || isUndefinedTable(err) {
		return fmt.Errorf("%w: no migrations have been applied at all — run `make migrate-up` "+
			"(or `migrate -path db/migrations -database \"$DB_URL\" up`) before starting the backend",
			ErrSchemaOutdated)
	}
	if err != nil {
		return fmt.Errorf("read schema version: %w", err)
	}
	if dirty {
		return fmt.Errorf("%w: migration %d is marked DIRTY, which means it failed part way. "+
			"Inspect it, fix the database, then `migrate ... force %d` before starting",
			ErrSchemaOutdated, version, version)
	}
	if version < RequiredSchemaVersion {
		return fmt.Errorf("%w: database is at migration %d, this build needs %d — "+
			"run `make migrate-up` before starting the backend",
			ErrSchemaOutdated, version, RequiredSchemaVersion)
	}
	return nil
}

// isUndefinedTable reports whether err is Postgres 42P01.
//
// A database that has never been migrated has no schema_migrations table at
// all, which is the commonest shape of this failure — a fresh volume, or a
// DB_URL pointing at the wrong database.
func isUndefinedTable(err error) bool {
	var pgErr interface{ SQLState() string }
	return errors.As(err, &pgErr) && pgErr.SQLState() == "42P01"
}
