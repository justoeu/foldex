package pgerr_test

import (
	"errors"
	"fmt"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/stretchr/testify/require"

	"foldex/internal/pkg/pgerr"
)

func TestUniqueConstraint(t *testing.T) {
	t.Parallel()
	require.Equal(t, "", pgerr.UniqueConstraint(nil))
	require.Equal(t, "", pgerr.UniqueConstraint(errors.New("nope")))

	pg := &pgconn.PgError{Code: "23505", ConstraintName: "link_slug_unique"}
	require.Equal(t, "link_slug_unique", pgerr.UniqueConstraint(pg))
	require.Equal(t, "link_slug_unique", pgerr.UniqueConstraint(fmt.Errorf("wrap: %w", pg)))

	other := &pgconn.PgError{Code: "23503", ConstraintName: "fk"}
	require.Equal(t, "", pgerr.UniqueConstraint(other))
}
