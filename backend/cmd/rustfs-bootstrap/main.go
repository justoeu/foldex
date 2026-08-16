// rustfs-bootstrap provisions a least-privilege app user on RustFS (S3 admin API).
//
// It is intended as a one-shot init container (see docker-compose.services.yml):
//  1. wait until the S3 endpoint answers
//  2. ensure the app bucket exists (as root)
//  3. create the app IAM user (idempotent)
//  4. install a canned policy scoped to that bucket only
//  5. attach the policy to the app user
//
// Root credentials never leave the init path; the backend runs as the app user.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os"
	"strings"
	"time"

	"github.com/minio/madmin-go/v3"
	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
)

type appProbeClient interface {
	PutObject(context.Context, string, string, io.Reader, int64, minio.PutObjectOptions) (minio.UploadInfo, error)
	RemoveObject(context.Context, string, string, minio.RemoveObjectOptions) error
}

func main() {
	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))
	cfg, err := loadCfg()
	if err != nil {
		logger.Error("invalid config", "err", err)
		os.Exit(2)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	if err := run(ctx, cfg, logger); err != nil {
		logger.Error("bootstrap failed", "err", err)
		os.Exit(1)
	}
	logger.Info("rustfs bootstrap ok",
		"endpoint", cfg.Endpoint,
		"bucket", cfg.Bucket,
		"app_user", cfg.AppAccessKey,
	)
}

type cfg struct {
	Endpoint                    string
	UseSSL                      bool
	RootAccess                  string
	RootSecret                  string
	AppAccessKey                string
	AppSecretKey                string
	Bucket                      string
	PolicyName                  string
	AllowInsecureDevCredentials bool
}

func loadCfg() (cfg, error) {
	c := cfg{
		Endpoint:                    envOr("RUSTFS_ENDPOINT", "localhost:9000"),
		UseSSL:                      envBool("RUSTFS_USE_SSL", false),
		RootAccess:                  envFirst("RUSTFS_ROOT_ACCESS_KEY", "RUSTFS_ROOT_USER", ""),
		RootSecret:                  envFirst("RUSTFS_ROOT_SECRET_KEY", "RUSTFS_ROOT_PASSWORD", ""),
		AppAccessKey:                envOr("RUSTFS_ACCESS_KEY", "foldex"),
		AppSecretKey:                envOr("RUSTFS_SECRET_KEY", ""),
		Bucket:                      envOr("RUSTFS_BUCKET", "foldex-screenshots"),
		PolicyName:                  envOr("RUSTFS_APP_POLICY", "foldex-app"),
		AllowInsecureDevCredentials: envBool("RUSTFS_ALLOW_INSECURE_DEV_CREDENTIALS", false),
	}
	// Strip scheme if an operator pasted a URL.
	c.Endpoint = strings.TrimPrefix(strings.TrimPrefix(c.Endpoint, "https://"), "http://")
	c.Endpoint = strings.TrimSuffix(c.Endpoint, "/")

	if c.RootAccess == "" || c.RootSecret == "" {
		return c, fmt.Errorf("RUSTFS_ROOT_ACCESS_KEY and RUSTFS_ROOT_SECRET_KEY are required")
	}
	if c.AppAccessKey == "" || c.AppSecretKey == "" {
		return c, fmt.Errorf("RUSTFS_ACCESS_KEY and RUSTFS_SECRET_KEY (app user) are required")
	}
	if !c.AllowInsecureDevCredentials && (knownPlaceholder(c.RootSecret) || knownPlaceholder(c.AppSecretKey)) {
		return c, fmt.Errorf("RustFS credential placeholder refused; run make env to generate credentials or explicitly set RUSTFS_ALLOW_INSECURE_DEV_CREDENTIALS=1 for isolated local development")
	}
	if c.AppAccessKey == c.RootAccess {
		return c, fmt.Errorf("app access key must differ from root access key")
	}
	if c.Bucket == "" {
		return c, fmt.Errorf("RUSTFS_BUCKET is required")
	}
	return c, nil
}

func run(ctx context.Context, c cfg, logger *slog.Logger) error {
	// Wait for the S3 port to accept root credentials.
	s3, err := waitS3(ctx, c, logger)
	if err != nil {
		return err
	}

	exists, err := s3.BucketExists(ctx, c.Bucket)
	if err != nil {
		return fmt.Errorf("bucket exists: %w", err)
	}
	if !exists {
		if err := s3.MakeBucket(ctx, c.Bucket, minio.MakeBucketOptions{}); err != nil {
			return fmt.Errorf("make bucket %q: %w", c.Bucket, err)
		}
		logger.Info("created bucket", "bucket", c.Bucket)
	} else {
		logger.Info("bucket already exists", "bucket", c.Bucket)
	}

	mad, err := madmin.New(c.Endpoint, c.RootAccess, c.RootSecret, c.UseSSL)
	if err != nil {
		return fmt.Errorf("admin client: %w", err)
	}

	if err := mad.AddUser(ctx, c.AppAccessKey, c.AppSecretKey); err != nil {
		// Idempotent: user already present is fine. madmin surfaces this as a
		// plain error string — treat "exists"/"already" as success.
		if !isAlreadyExists(err) {
			return fmt.Errorf("add user %q: %w", c.AppAccessKey, err)
		}
		// Refresh secret so rotating RUSTFS_SECRET_KEY takes effect on re-run.
		if err := mad.SetUser(ctx, c.AppAccessKey, c.AppSecretKey, madmin.AccountEnabled); err != nil {
			return fmt.Errorf("update user %q: %w", c.AppAccessKey, err)
		}
		logger.Info("app user already present — secret refreshed", "user", c.AppAccessKey)
	} else {
		logger.Info("created app user", "user", c.AppAccessKey)
	}

	policyDoc, err := bucketPolicy(c.Bucket)
	if err != nil {
		return err
	}
	if err := mad.AddCannedPolicy(ctx, c.PolicyName, policyDoc); err != nil {
		return fmt.Errorf("add policy %q: %w", c.PolicyName, err)
	}
	logger.Info("installed canned policy", "policy", c.PolicyName, "bucket", c.Bucket)

	if _, err := mad.AttachPolicy(ctx, madmin.PolicyAssociationReq{
		Policies: []string{c.PolicyName},
		User:     c.AppAccessKey,
	}); err != nil {
		return fmt.Errorf("attach policy %q to %q: %w", c.PolicyName, c.AppAccessKey, err)
	}
	logger.Info("attached policy to app user", "policy", c.PolicyName, "user", c.AppAccessKey)

	// Prove the app principal can write (and only to its bucket).
	app, err := minio.New(c.Endpoint, &minio.Options{
		Creds:  credentials.NewStaticV4(c.AppAccessKey, c.AppSecretKey, ""),
		Secure: c.UseSSL,
	})
	if err != nil {
		return fmt.Errorf("app s3 client: %w", err)
	}
	return probeAppCapabilities(ctx, app, c.Bucket)
}

func probeAppCapabilities(ctx context.Context, app appProbeClient, bucket string) error {
	const probeKey = ".foldex-bootstrap-probe"
	if _, err := app.PutObject(ctx, bucket, probeKey, strings.NewReader("ok"), 2,
		minio.PutObjectOptions{ContentType: "text/plain"}); err != nil {
		return fmt.Errorf("app user cannot write to %q: %w", bucket, err)
	}
	if err := app.RemoveObject(ctx, bucket, probeKey, minio.RemoveObjectOptions{}); err != nil {
		return fmt.Errorf("app user cannot delete from %q: %w", bucket, err)
	}
	return nil
}

func waitS3(ctx context.Context, c cfg, logger *slog.Logger) (*minio.Client, error) {
	var last error
	for {
		select {
		case <-ctx.Done():
			if last == nil {
				last = ctx.Err()
			}
			return nil, fmt.Errorf("timeout waiting for rustfs at %s: %w", c.Endpoint, last)
		default:
		}
		cli, err := minio.New(c.Endpoint, &minio.Options{
			Creds:  credentials.NewStaticV4(c.RootAccess, c.RootSecret, ""),
			Secure: c.UseSSL,
		})
		if err != nil {
			last = err
		} else {
			// ListBuckets is a cheap authenticated probe.
			if _, err := cli.ListBuckets(ctx); err == nil {
				return cli, nil
			} else {
				last = err
			}
		}
		logger.Info("waiting for rustfs", "endpoint", c.Endpoint, "err", last)
		time.Sleep(2 * time.Second)
	}
}

func bucketPolicy(bucket string) ([]byte, error) {
	doc := map[string]any{
		"Version": "2012-10-17",
		"Statement": []map[string]any{
			{
				"Effect": "Allow",
				"Action": []string{
					"s3:GetBucketLocation",
					"s3:ListBucket",
					"s3:ListBucketMultipartUploads",
				},
				"Resource": []string{"arn:aws:s3:::" + bucket},
			},
			{
				"Effect": "Allow",
				"Action": []string{
					"s3:AbortMultipartUpload",
					"s3:DeleteObject",
					"s3:GetObject",
					"s3:ListMultipartUploadParts",
					"s3:PutObject",
				},
				"Resource": []string{"arn:aws:s3:::" + bucket + "/*"},
			},
		},
	}
	return json.Marshal(doc)
}

func knownPlaceholder(secret string) bool {
	return secret == "rustfsadmin" || secret == "foldex-change-me"
}

func isAlreadyExists(err error) bool {
	if err == nil {
		return false
	}
	s := strings.ToLower(err.Error())
	return strings.Contains(s, "already") ||
		strings.Contains(s, "exists") ||
		strings.Contains(s, "xminioadminuserexists")
}

func envOr(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

func envFirst(keys ...string) string {
	for _, k := range keys {
		if k == "" {
			continue
		}
		if v := os.Getenv(k); v != "" {
			return v
		}
	}
	return ""
}

func envBool(k string, def bool) bool {
	v := os.Getenv(k)
	if v == "" {
		return def
	}
	switch strings.ToLower(v) {
	case "1", "true", "yes", "on":
		return true
	case "0", "false", "no", "off":
		return false
	default:
		return def
	}
}
