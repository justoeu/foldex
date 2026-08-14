package auth

import (
	"errors"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/stretchr/testify/assert"
)

func TestMapIdentityConflictPreservesUnexpectedErrors(t *testing.T) {
	t.Parallel()

	assert.NoError(t, mapIdentityConflict(nil))
	assert.ErrorContains(t, mapIdentityConflict(errors.New("database unavailable")), "database unavailable")
	pgErr := &pgconn.PgError{
		Code: "23505", ConstraintName: "unexpected_unique_constraint",
	}
	assert.ErrorIs(t, mapIdentityConflict(pgErr), pgErr)
}

func TestNullString(t *testing.T) {
	t.Parallel()

	assert.Nil(t, nullString(""))
	if value := nullString("value"); assert.NotNil(t, value) {
		assert.Equal(t, "value", *value)
	}
}
