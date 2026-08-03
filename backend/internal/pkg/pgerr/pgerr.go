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
