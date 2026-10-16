// Package pgerr extracts structured fields from pgx/pgconn errors.
package pgerr

import (
	"errors"

	"github.com/jackc/pgx/v5/pgconn"
)

// UniqueConstraint returns the violated constraint name when err is a
// Postgres 23505 unique-violation, or "" otherwise. errors.As survives %w
// wrapping — string-matching the formatted message would silently break if
// any wrapping layer omitted Unwrap.
func UniqueConstraint(err error) string {
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) || pgErr.Code != "23505" {
		return ""
	}
	return pgErr.ConstraintName
}

// IsUndefinedTable reports a Postgres 42P01 (undefined_table) — the one
// schema-gap error a reader may legitimately degrade past; every other SQL
// failure must surface instead of being mistaken for an absent table.
func IsUndefinedTable(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "42P01"
}

// IsSlugRace reports a unique-violation on one of the GLOBAL slug indexes.
// Slugs are allocated from a preloaded taken-set, so a concurrent create can
// win the race between preload and insert; callers retry the whole
// transaction (bounded) when this matches.
func IsSlugRace(err error) bool {
	switch UniqueConstraint(err) {
	case "link_slug_unique", "note_slug_unique":
		return true
	}
	return false
}
