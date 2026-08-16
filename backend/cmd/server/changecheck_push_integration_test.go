//go:build integration

package main

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"sync"
	"testing"
	"time"

	webpush "github.com/SherClockHolmes/webpush-go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"foldex/internal/changecheck"
	"foldex/internal/links"
	"foldex/internal/push"
	"foldex/internal/testdb"
)

type recordingPushNotifier struct {
	mu        sync.Mutex
	endpoints []string
}

func (n *recordingPushNotifier) notify(
	_ context.Context,
	_ []byte,
	sub *webpush.Subscription,
	_ *webpush.Options,
) (*http.Response, error) {
	n.mu.Lock()
	n.endpoints = append(n.endpoints, sub.Endpoint)
	n.mu.Unlock()
	return &http.Response{StatusCode: http.StatusCreated, Body: http.NoBody, Header: make(http.Header)}, nil
}

func (n *recordingPushNotifier) snapshot() []string {
	n.mu.Lock()
	defer n.mu.Unlock()
	return append([]string(nil), n.endpoints...)
}

type staticChangeFetcher struct{ body []byte }

func (f staticChangeFetcher) GetRaw(context.Context, string) ([]byte, string, error) {
	return f.body, "text/html", nil
}

func TestChangeCheckPushGoesOnlyToTheLinkOwner(t *testing.T) {
	pool := testdb.New(t)
	ctx := context.Background()
	ownerA := testdb.SeedUser(t, pool, "owner-a@test.local", "user")
	ownerB := testdb.SeedUser(t, pool, "owner-b@test.local", "user")

	linkRepo := links.NewRepository(pool)
	linkA, err := linkRepo.Create(ctx, ownerA, links.CreateInput{
		URL: "https://owner-a.test/page", Title: "Owner A page",
	})
	require.NoError(t, err)
	daily := "daily"
	_, err = linkRepo.Update(ctx, ownerA, linkA.ID, links.UpdateInput{
		CheckInterval: &daily, CheckIntervalSet: true,
	})
	require.NoError(t, err)

	oldBody := []byte(`<html><body><main>old owner A content</main></body></html>`)
	newBody := []byte(`<html><body><main>new owner A content</main></body></html>`)
	fetcher := staticChangeFetcher{body: newBody}
	kind, hash, err := changecheck.NewFingerprinter(fetcher).Compute(ctx, linkA.URL, oldBody)
	require.NoError(t, err)
	due, err := linkRepo.SystemFindDueForCheck(ctx, 1)
	require.NoError(t, err)
	require.Len(t, due, 1)
	applied, err := linkRepo.SystemRecordCheckResult(ctx, linkA.ID, due[0].ClaimedAt, links.CheckResult{
		Fingerprint: changecheck.FormatFingerprint(kind, hash),
	})
	require.NoError(t, err)
	require.True(t, applied)
	_, err = pool.Exec(ctx,
		`UPDATE link SET last_checked_at = now() - interval '2 days' WHERE user_id = $1 AND id = $2`,
		int64(ownerA), linkA.ID)
	require.NoError(t, err)

	pushRepo := push.NewRepository(pool)
	const endpointA = "https://push.example/owner-a"
	const endpointB = "https://push.example/owner-b"
	_, err = pushRepo.Save(ctx, ownerA, endpointA, "owner-a-key", "owner-a-auth")
	require.NoError(t, err)
	_, err = pushRepo.Save(ctx, ownerB, endpointB, "owner-b-key", "owner-b-auth")
	require.NoError(t, err)

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	notifier := &recordingPushNotifier{}
	sender := push.NewSender(push.VAPIDKeys{
		PublicKey: "test-public", PrivateKey: "test-private", Subject: "mailto:test@example.com",
	}, pushRepo, logger).WithNotifyFunc(notifier.notify)
	worker := changecheck.New(linkRepo, fetcher, pushSenderAdapter{s: sender}, changecheck.Options{
		Concurrency: 1, ScanInterval: time.Hour, FetchTimeout: time.Second,
	}, logger)
	worker.Start(ctx)
	t.Cleanup(worker.Stop)

	require.Eventually(t, func() bool {
		return len(notifier.snapshot()) == 1
	}, 3*time.Second, 10*time.Millisecond)
	require.Eventually(t, func() bool {
		subs, listErr := pushRepo.List(ctx, ownerA)
		return listErr == nil && len(subs) == 1 && subs[0].LastUsedAt != nil
	}, 3*time.Second, 10*time.Millisecond)
	worker.Stop()

	assert.Equal(t, []string{endpointA}, notifier.snapshot())
	subsA, err := pushRepo.List(ctx, ownerA)
	require.NoError(t, err)
	require.Len(t, subsA, 1)
	assert.NotNil(t, subsA[0].LastUsedAt)
	subsB, err := pushRepo.List(ctx, ownerB)
	require.NoError(t, err)
	require.Len(t, subsB, 1)
	assert.Nil(t, subsB[0].LastUsedAt)
}
