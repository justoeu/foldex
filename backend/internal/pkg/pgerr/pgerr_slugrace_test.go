package pgerr_test

import (
	"errors"
	"fmt"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/stretchr/testify/assert"

	"foldex/internal/pkg/pgerr"
)

func TestIsSlugRace_MatchesOnlySlugConstraints(t *testing.T) {
	slugErr := &pgconn.PgError{Code: "23505", ConstraintName: "link_slug_unique"}
	assert.True(t, pgerr.IsSlugRace(fmt.Errorf("insert: %w", slugErr)))
	assert.True(t, pgerr.IsSlugRace(&pgconn.PgError{Code: "23505", ConstraintName: "note_slug_unique"}))
	assert.False(t, pgerr.IsSlugRace(&pgconn.PgError{Code: "23505", ConstraintName: "link_url_unique"}))
	assert.False(t, pgerr.IsSlugRace(&pgconn.PgError{Code: "23503", ConstraintName: "link_slug_unique"}))
	assert.False(t, pgerr.IsSlugRace(errors.New("boom")))
	assert.False(t, pgerr.IsSlugRace(nil))
}
