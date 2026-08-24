//go:build integration

package policy_test

import (
	"bytes"
	"context"
	"log/slog"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"foldex/internal/policy"
	"foldex/internal/testdb"
)

// An instance configured above bcrypt's truncation point refuses every
// password and nothing else anywhere says why — this log line is the only
// thing that tells the operator to lower it.
func TestWarnUnenforceableFloor(t *testing.T) {
	ctx := context.Background()
	pool := testdb.New(t)
	repo := policy.NewRepository(pool)

	var buf bytes.Buffer
	log := slog.New(slog.NewTextHandler(&buf, nil))

	// Quiet on a default instance: a warning that fires when nothing is wrong
	// is a warning operators learn to ignore.
	repo.WarnUnenforceableFloor(ctx, log)
	require.Empty(t, buf.String())

	// A floor no password can satisfy can only be reached by a document
	// written before the write bound tightened, so it is planted directly.
	_, err := pool.Exec(ctx,
		`INSERT INTO app_setting (key, value) VALUES ('instance_policy', $1)
		 ON CONFLICT (key) DO UPDATE SET value = EXCLUDED.value`,
		`{"password_min_length":100,"otp_ttl_minutes":5,"otp_cooldown_seconds":60,`+
			`"google_allowed_domains":[],"google_auto_provision":false,`+
			`"google_default_role":"editor","admin_second_factor":"any"}`)
	require.NoError(t, err)

	repo.WarnUnenforceableFloor(ctx, log)
	out := buf.String()
	assert.Contains(t, out, "cannot be satisfied")
	assert.Contains(t, out, "100", "the warning must name the configured value")
	assert.Contains(t, out, "72", "and the enforceable ceiling")
}
