package pgerr_test

import (
	"errors"
	"fmt"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/stretchr/testify/assert"

	"foldex/internal/pkg/pgerr"
)

func TestIsUndefinedTable(t *testing.T) {
	t.Parallel()
	undefined := &pgconn.PgError{Code: "42P01"}

	cases := []struct {
		name string
		err  error
		want bool
	}{
		{name: "bare 42P01", err: undefined, want: true},
		{name: "wrapped 42P01", err: fmt.Errorf("select feed state: %w", undefined), want: true},
		{name: "doubly wrapped 42P01", err: fmt.Errorf("outer: %w", fmt.Errorf("inner: %w", undefined)), want: true},
		{name: "nil", err: nil, want: false},
		{name: "plain error", err: errors.New("relation does not exist"), want: false},
		{name: "unique violation", err: &pgconn.PgError{Code: "23505"}, want: false},
		{name: "undefined column is a different code", err: &pgconn.PgError{Code: "42703"}, want: false},
		{name: "wrapped other code", err: fmt.Errorf("wrap: %w", &pgconn.PgError{Code: "23503"}), want: false},
		{name: "empty code", err: &pgconn.PgError{}, want: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tc.want, pgerr.IsUndefinedTable(tc.err))
		})
	}
}
