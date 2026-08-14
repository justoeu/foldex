package main

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func setBootstrapEnv(t *testing.T, rootSecret, appSecret string) {
	t.Helper()
	t.Setenv("RUSTFS_ENDPOINT", "localhost:9000")
	t.Setenv("RUSTFS_ROOT_ACCESS_KEY", "root")
	t.Setenv("RUSTFS_ROOT_SECRET_KEY", rootSecret)
	t.Setenv("RUSTFS_ACCESS_KEY", "app")
	t.Setenv("RUSTFS_SECRET_KEY", appSecret)
	t.Setenv("RUSTFS_BUCKET", "foldex-test")
	t.Setenv("RUSTFS_ALLOW_INSECURE_DEV_CREDENTIALS", "")
}

func TestLoadCfgRejectsKnownCredentialPlaceholders(t *testing.T) {
	t.Run("root", func(t *testing.T) {
		setBootstrapEnv(t, "rustfsadmin", "safe-app-secret")
		_, err := loadCfg()
		require.ErrorContains(t, err, "placeholder")
		assert.NotContains(t, err.Error(), "rustfsadmin")
	})

	t.Run("app", func(t *testing.T) {
		setBootstrapEnv(t, "safe-root-secret", "foldex-change-me")
		_, err := loadCfg()
		require.ErrorContains(t, err, "placeholder")
		assert.NotContains(t, err.Error(), "foldex-change-me")
	})

	t.Run("explicit local dev opt-in", func(t *testing.T) {
		setBootstrapEnv(t, "rustfsadmin", "foldex-change-me")
		t.Setenv("RUSTFS_ALLOW_INSECURE_DEV_CREDENTIALS", "1")
		_, err := loadCfg()
		require.NoError(t, err)
	})
}

func TestBucketPolicyContainsOnlyRuntimeActions(t *testing.T) {
	raw, err := bucketPolicy("foldex-test")
	require.NoError(t, err)

	var doc struct {
		Statement []struct {
			Action   []string
			Resource []string
		}
	}
	require.NoError(t, json.Unmarshal(raw, &doc))
	require.Len(t, doc.Statement, 2)

	assert.ElementsMatch(t, []string{
		"s3:GetBucketLocation",
		"s3:ListBucket",
		"s3:ListBucketMultipartUploads",
	}, doc.Statement[0].Action)
	assert.Equal(t, []string{"arn:aws:s3:::foldex-test"}, doc.Statement[0].Resource)

	assert.ElementsMatch(t, []string{
		"s3:AbortMultipartUpload",
		"s3:DeleteObject",
		"s3:GetObject",
		"s3:ListMultipartUploadParts",
		"s3:PutObject",
	}, doc.Statement[1].Action)
	assert.Equal(t, []string{"arn:aws:s3:::foldex-test/*"}, doc.Statement[1].Resource)

	for _, statement := range doc.Statement {
		assert.NotContains(t, statement.Action, "s3:*")
	}
}
