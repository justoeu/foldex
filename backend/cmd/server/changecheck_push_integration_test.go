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
	ownerA := testdb.SeedUser(t, pool, "owner-a@test.local", "editor")
	ownerB := testdb.SeedUser(t, pool, "owner-b@test.local", "editor")

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

// The second half of the disabled-owner rule, asserted where it actually
// matters: no bytes leave for a disabled account.
//
// The repository test proves push.Repository.List filters. That is a claim
// about a query; this is a claim about DELIVERY, and they are only the same
// thing while Sender.Notify keeps consulting List at send time. Nothing failed
// if that stopped being true — any future batching (subscriptions gathered at
// claim time, a cached store, a multi-user fan-out) would re-open the window
// with every repository test still green.
//
// It is also the window the two-sided filter exists for: the claim's snapshot
// says nothing about the present, so an account disabled AFTER its link was
// claimed still had a notification in flight. Here that disable happens
// between the sanity delivery and the second one.
func TestNotify_StopsAtTheDoorForADisabledOwner(t *testing.T) {
	pool := testdb.New(t)
	ctx := context.Background()
	owner := testdb.SeedUser(t, pool, "soon-disabled@test.local", "editor")
	peer := testdb.SeedUser(t, pool, "stays-active@test.local", "editor")

	pushRepo := push.NewRepository(pool)
	_, err := pushRepo.Save(ctx, owner, "https://push.example/soon-disabled", "k", "a")
	require.NoError(t, err)
	_, err = pushRepo.Save(ctx, peer, "https://push.example/stays-active", "k", "a")
	require.NoError(t, err)

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	notifier := &recordingPushNotifier{}
	sender := push.NewSender(push.VAPIDKeys{
		PublicKey: "test-public", PrivateKey: "test-private", Subject: "mailto:test@example.com",
	}, pushRepo, logger).WithNotifyFunc(notifier.notify)

	require.NoError(t, sender.Notify(ctx, push.Notification{UserID: owner, Title: "before"}))
	require.Len(t, notifier.snapshot(), 1, "an active owner is delivered to")

	_, err = pool.Exec(ctx, `UPDATE app_user SET status = 'disabled' WHERE id = $1`, int64(owner))
	require.NoError(t, err)

	require.NoError(t, sender.Notify(ctx, push.Notification{UserID: owner, Title: "after"}),
		"a disabled owner is a no-op, not an error — the caller did nothing wrong")
	assert.Len(t, notifier.snapshot(), 1, "nothing may be sent after the account was disabled")

	// The peer proves the send path is still working, so the assertion above is
	// not passing because the sender broke.
	require.NoError(t, sender.Notify(ctx, push.Notification{UserID: peer, Title: "peer"}))
	assert.Len(t, notifier.snapshot(), 2, "an unrelated active account still receives")
}
