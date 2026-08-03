package folders

import (
	"fmt"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/stretchr/testify/assert"
)

func TestIsSerializationFailure(t *testing.T) {
	assert.False(t, isSerializationFailure(nil))
	assert.False(t, isSerializationFailure(fmt.Errorf("nope")))
	assert.False(t, isSerializationFailure(&pgconn.PgError{Code: "23505"}))

	pg := &pgconn.PgError{Code: "40001"}
	assert.True(t, isSerializationFailure(pg))
	assert.True(t, isSerializationFailure(fmt.Errorf("commit update folder: %w", pg)))
}
