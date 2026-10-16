package importer

import (
	"errors"
	"fmt"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/stretchr/testify/assert"
)

func TestIsSlugRace_MatchesOnlySlugConstraints(t *testing.T) {
	slugErr := &pgconn.PgError{Code: "23505", ConstraintName: "link_slug_unique"}
	assert.True(t, isSlugRace(fmt.Errorf("insert: %w", slugErr)))
	assert.True(t, isSlugRace(&pgconn.PgError{Code: "23505", ConstraintName: "note_slug_unique"}))
	assert.False(t, isSlugRace(&pgconn.PgError{Code: "23505", ConstraintName: "link_url_unique"}))
	assert.False(t, isSlugRace(&pgconn.PgError{Code: "23503", ConstraintName: "link_slug_unique"}))
	assert.False(t, isSlugRace(errors.New("boom")))
	assert.False(t, isSlugRace(nil))
}
