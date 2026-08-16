package screenshot

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/go-rod/rod"
	"github.com/go-rod/rod/lib/launcher/flags"
	"github.com/go-rod/rod/lib/proto"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPoolClose_NilAndIdleSafe(t *testing.T) {
	var nilPool *Pool
	nilPool.Close()
	pool := NewPool()
	pool.Close()
	pool.Close()
}

func TestCapture_CancelledContext(t *testing.T) {
	pool := NewPool()
	pool.captureSem <- struct{}{}
	defer func() { <-pool.captureSem }()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := pool.Capture(ctx, "https://example.com")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "wait for slot")
}

func TestCapture_QueueBudgetExpiresWithLiveCaller(t *testing.T) {
	pool := NewPool()
	pool.queueTimeout = 5 * time.Millisecond
	pool.captureSem <- struct{}{}
	defer func() { <-pool.captureSem }()

	_, err := pool.Capture(context.Background(), "https://example.com")
	require.ErrorIs(t, err, context.DeadlineExceeded)
	assert.Contains(t, err.Error(), "wait for slot")
}

func TestCloseRejectsQueuedCaptureImmediately(t *testing.T) {
	pool := NewPool()
	pool.queueTimeout = time.Second
	pool.captureSem <- struct{}{}
	result := make(chan error, 1)
	go func() {
		_, err := pool.Capture(context.Background(), "https://example.com")
		result <- err
	}()

	pool.Close()
	select {
	case err := <-result:
		require.ErrorIs(t, err, errPoolClosed)
	case <-time.After(100 * time.Millisecond):
		t.Fatal("queued capture did not observe pool shutdown")
	}
	<-pool.captureSem
}

func TestCapture_RejectsNonHTTPTargetBeforeBrowserLaunch(t *testing.T) {
	pool := NewPool()
	t.Setenv("CHROME_PATH", "/nonexistent/chrome-binary-xyz")
	for _, target := range []string{"file:///etc/passwd", "data:text/html,x", "about:blank", "http:///missing-host"} {
		_, err := pool.Capture(context.Background(), target)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "invalid http(s) target", target)
	}
}

func TestConfiguredLauncher_DisablesDirectUDPBypasses(t *testing.T) {
	configured := configuredLauncherWithProxy("127.0.0.1:43210")
	args := configured.FormatArgs()
	for _, flag := range []string{
		"--force-webrtc-ip-handling-policy=disable_non_proxied_udp",
		"--disable-webrtc-multiple-routes",
		"--disable-quic",
		"--proxy-server=127.0.0.1:43210",
		"--proxy-bypass-list=<-loopback>",
	} {
		assert.True(t, slices.Contains(args, flag), "missing Chromium launcher contract %s", flag)
	}
	assert.False(t, slices.Contains(args, "--webrtc-ip-handling-policy=disable_non_proxied_udp"))
	assert.False(t, slices.Contains(args, "--no-sandbox"), "Chromium must retain its sandbox")
	assert.False(t, configured.Has(flags.Leakless), "leakless cannot honor startup cancellation")
}

func TestCaptureSerializesProcessProxyAttribution(t *testing.T) {
	assert.Equal(t, 1, cap(NewPool().captureSem))
}

type fakeBrowserLauncher struct {
	url       string
	launchErr error
	launch    func(context.Context) (string, error)
	cleanup   func(context.Context) error
	pid       int
	kills     atomic.Int64
	cleanups  atomic.Int64
}

func (f *fakeBrowserLauncher) Launch(ctx context.Context) (string, error) {
	if f.launch != nil {
		return f.launch(ctx)
	}
	return f.url, f.launchErr
}
func (f *fakeBrowserLauncher) PID() int { return f.pid }
func (f *fakeBrowserLauncher) Kill()    { f.kills.Add(1) }
func (f *fakeBrowserLauncher) Cleanup(ctx context.Context) error {
	f.cleanups.Add(1)
	if f.cleanup != nil {
		return f.cleanup(ctx)
	}
	return nil
}

func TestAcquireBrowser_BadChromePath(t *testing.T) {
	pool := NewPool()
	t.Setenv("CHROME_PATH", "/nonexistent/chrome-binary-xyz")
	_, _, err := pool.acquireBrowser(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "launch browser")
}

func TestCloseTimeoutKillsAndCleansLauncherExactlyOnce(t *testing.T) {
	pool := NewPool()
	fake := &fakeBrowserLauncher{pid: 4242}
	pool.closeTimeout = 5 * time.Millisecond
	pool.closeBrowser = func(ctx context.Context, _ *rod.Browser) error {
		<-ctx.Done()
		return ctx.Err()
	}
	pb := testGeneration(fake, 0)
	pool.current = pb
	pool.generations[pb] = struct{}{}

	pool.Close()
	pool.Close()

	assert.Equal(t, int64(1), fake.kills.Load())
	assert.Equal(t, int64(1), fake.cleanups.Load())
}

func TestConnectFailureKillsLaunchedProcess(t *testing.T) {
	pool := NewPool()
	fake := &fakeBrowserLauncher{url: "ws://127.0.0.1:9222/devtools/browser/test", pid: 4243}
	pool.newBrowserLauncher = func(string) browserLauncher { return fake }
	pool.connectBrowser = func(*rod.Browser) error { return errors.New("connect failed") }

	_, _, err := pool.acquireBrowser(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "connect browser")
	assert.Equal(t, int64(1), fake.kills.Load())
	assert.Equal(t, int64(1), fake.cleanups.Load())
}

func TestLaunchFailureCleansProfileWithAndWithoutPID(t *testing.T) {
	for _, pid := range []int{0, 4247} {
		pool := NewPool()
		fake := &fakeBrowserLauncher{launchErr: errors.New("launch failed"), pid: pid}
		_, _, err := pool.launchBrowser(context.Background(), fake, nil)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "launch browser")
		assert.Equal(t, int64(boolInt(pid != 0)), fake.kills.Load())
		assert.Equal(t, int64(1), fake.cleanups.Load())
	}
}

func TestLaunchFailureRetriesTransientProfileCleanup(t *testing.T) {
	pool := NewPool()
	fake := &fakeBrowserLauncher{launchErr: errors.New("launch failed"), pid: 4247}
	fake.cleanup = func(context.Context) error {
		if fake.cleanups.Load() == 1 {
			return errors.New("profile busy")
		}
		return nil
	}

	_, _, err := pool.launchBrowser(context.Background(), fake, nil)
	require.Error(t, err)
	assert.Equal(t, int64(1), fake.kills.Load())
	assert.Equal(t, int64(2), fake.cleanups.Load())
}

func TestAcquireBrowserStartupIsBoundedAndDoesNotHoldPoolLock(t *testing.T) {
	pool := NewPool()
	started := make(chan struct{})
	fake := &fakeBrowserLauncher{launch: func(ctx context.Context) (string, error) {
		close(started)
		<-ctx.Done()
		return "", ctx.Err()
	}}
	pool.newBrowserLauncher = func(string) browserLauncher { return fake }
	pool.startupTimeout = 100 * time.Millisecond
	result := make(chan error, 1)
	go func() {
		_, _, err := pool.acquireBrowser(context.Background())
		result <- err
	}()
	awaitSignal(t, started, "browser launch did not start")

	locked := make(chan struct{})
	go func() {
		pool.mu.Lock()
		pool.mu.Unlock()
		close(locked)
	}()
	awaitSignal(t, locked, "browser startup held pool mutex")
	select {
	case err := <-result:
		require.ErrorIs(t, err, context.DeadlineExceeded)
	case <-time.After(250 * time.Millisecond):
		t.Fatal("browser startup exceeded its bounded context")
	}
	assert.Equal(t, int64(1), fake.cleanups.Load())
}

func TestAcquireBrowserConnectIsBoundedAndCleansProcess(t *testing.T) {
	pool := NewPool()
	connectStarted := make(chan struct{})
	fake := &fakeBrowserLauncher{url: "ws://127.0.0.1:9222/devtools/browser/test", pid: 4249}
	pool.newBrowserLauncher = func(string) browserLauncher { return fake }
	pool.connectBrowser = func(browser *rod.Browser) error {
		close(connectStarted)
		<-browser.GetContext().Done()
		return browser.GetContext().Err()
	}
	pool.startupTimeout = 100 * time.Millisecond
	result := make(chan error, 1)
	go func() {
		_, _, err := pool.acquireBrowser(context.Background())
		result <- err
	}()
	awaitSignal(t, connectStarted, "browser connect did not start")

	select {
	case err := <-result:
		require.ErrorIs(t, err, context.DeadlineExceeded)
	case <-time.After(250 * time.Millisecond):
		t.Fatal("browser connect exceeded its bounded context")
	}
	assert.Equal(t, int64(1), fake.kills.Load())
	assert.Equal(t, int64(1), fake.cleanups.Load())
}

func TestCloseCancelsBrowserStartup(t *testing.T) {
	pool := NewPool()
	started := make(chan struct{})
	fake := &fakeBrowserLauncher{pid: 4250, launch: func(ctx context.Context) (string, error) {
		close(started)
		<-ctx.Done()
		return "", ctx.Err()
	}}
	pool.newBrowserLauncher = func(string) browserLauncher { return fake }
	result := make(chan error, 1)
	go func() {
		_, _, err := pool.acquireBrowser(context.Background())
		result <- err
	}()
	awaitSignal(t, started, "browser launch did not start")
	pool.Close()

	assert.Equal(t, int64(1), fake.kills.Load())
	assert.Equal(t, int64(1), fake.cleanups.Load())
	select {
	case err := <-result:
		require.ErrorIs(t, err, context.Canceled)
	case <-time.After(250 * time.Millisecond):
		t.Fatal("Close did not cancel browser startup")
	}
}

func TestCloseCleansStartupThatReturnsAfterCancellation(t *testing.T) {
	pool := NewPool()
	started := make(chan struct{})
	release := make(chan struct{})
	fake := &fakeBrowserLauncher{pid: 4256, launch: func(context.Context) (string, error) {
		close(started)
		<-release
		return "ws://127.0.0.1:9222/devtools/browser/late", nil
	}}
	pool.newBrowserLauncher = func(string) browserLauncher { return fake }
	pool.connectBrowser = func(*rod.Browser) error { return nil }
	acquired := make(chan error, 1)
	go func() {
		_, _, err := pool.acquireBrowser(context.Background())
		acquired <- err
	}()
	awaitSignal(t, started, "browser launch did not start")

	closed := make(chan struct{})
	go func() {
		pool.Close()
		close(closed)
	}()
	select {
	case <-closed:
		t.Fatal("Close returned while cancelled startup still owned a process")
	case <-time.After(20 * time.Millisecond):
	}
	close(release)
	awaitSignal(t, closed, "Close did not join late startup cleanup")
	require.ErrorIs(t, <-acquired, context.Canceled)
	assert.Equal(t, int64(1), fake.kills.Load())
	assert.Equal(t, int64(1), fake.cleanups.Load())
}

func TestCloseForceStopsStartupThatIgnoresCancellation(t *testing.T) {
	pool := NewPool()
	pool.shutdownTimeout = 10 * time.Millisecond
	started := make(chan struct{})
	release := make(chan struct{})
	fake := &fakeBrowserLauncher{pid: 4258, launch: func(context.Context) (string, error) {
		close(started)
		<-release
		return "", context.Canceled
	}}
	pool.newBrowserLauncher = func(string) browserLauncher { return fake }
	result := make(chan error, 1)
	go func() {
		_, _, err := pool.acquireBrowser(context.Background())
		result <- err
	}()
	awaitSignal(t, started, "browser launch did not start")

	pool.Close()
	assert.Equal(t, int64(1), fake.kills.Load())
	assert.Equal(t, int64(1), fake.cleanups.Load())
	close(release)
	require.Error(t, <-result)
}

func TestCloseCancelsActiveCaptureAndWaitsForTeardown(t *testing.T) {
	pool := NewPool()
	fake := &fakeBrowserLauncher{pid: 4252}
	pool.closeBrowser = func(context.Context, *rod.Browser) error { return nil }
	cancelled := make(chan struct{})
	lifetimeCtx, lifetimeCancel := context.WithCancel(context.Background())
	pb := testGeneration(fake, 1)
	pb.cancel = func() {
		lifetimeCancel()
		select {
		case <-cancelled:
		default:
			close(cancelled)
		}
	}
	pool.current = pb
	pool.generations[pb] = struct{}{}

	released := make(chan struct{})
	go func() {
		<-lifetimeCtx.Done()
		pool.releaseBrowser(pb)
		close(released)
	}()
	pool.Close()

	awaitSignal(t, cancelled, "Close did not cancel the active generation")
	awaitSignal(t, released, "Close returned before active generation release")
	assert.Equal(t, int64(1), fake.cleanups.Load(), "Close must wait for profile cleanup")
	select {
	case <-pb.stopped:
	default:
		t.Fatal("Close returned before generation teardown completed")
	}
}

func TestCloseShutdownBudgetBoundsStalledTeardown(t *testing.T) {
	pool := NewPool()
	pool.shutdownTimeout = 10 * time.Millisecond
	started := make(chan struct{})
	release := make(chan struct{})
	fake := &fakeBrowserLauncher{pid: 4253}
	pool.closeBrowser = func(context.Context, *rod.Browser) error {
		close(started)
		<-release
		return nil
	}
	pb := testGeneration(fake, 0)
	pool.current = pb
	pool.generations[pb] = struct{}{}

	begin := time.Now()
	pool.Close()
	assert.Less(t, time.Since(begin), 100*time.Millisecond)
	awaitSignal(t, started, "generation teardown did not start")
	awaitSignal(t, pb.stopped, "shutdown deadline did not force generation teardown")
	assert.Equal(t, int64(1), fake.kills.Load())
	assert.Equal(t, int64(1), fake.cleanups.Load())
	close(release)
}

func TestCaptureQueueTimeDoesNotConsumeExecutionBudget(t *testing.T) {
	pool := NewPool()
	pool.queueTimeout = time.Second
	pool.executionTimeout = 200 * time.Millisecond
	remaining := make(chan time.Duration, 1)
	pool.createBrowserContext = func(context.Context, *rod.Browser, browserContextProxy) (proto.BrowserBrowserContextID, error) {
		return "budget-context", nil
	}
	pool.disposeBrowserContext = func(context.Context, *rod.Browser, proto.BrowserBrowserContextID) error { return nil }
	pool.captureBrowserContext = func(ctx context.Context, _ *rod.Browser, _ proto.BrowserBrowserContextID, _ string) ([]byte, error) {
		deadline, ok := ctx.Deadline()
		if !ok {
			return nil, errors.New("capture context has no deadline")
		}
		remaining <- time.Until(deadline)
		return []byte("png"), nil
	}
	pool.current = &pooledBrowser{browser: &rod.Browser{}, stopped: make(chan struct{})}
	pool.generations[pool.current] = struct{}{}

	pool.captureSem <- struct{}{}
	result := make(chan error, 1)
	go func() {
		_, err := pool.Capture(context.Background(), "http://93.184.216.34/page")
		result <- err
	}()
	time.Sleep(50 * time.Millisecond)
	<-pool.captureSem

	require.NoError(t, <-result)
	assert.Greater(t, <-remaining, 150*time.Millisecond)
}

func TestCaptureExecutionTimeoutCancelsBlockedWork(t *testing.T) {
	pool := NewPool()
	pool.executionTimeout = 20 * time.Millisecond
	cancelled := make(chan struct{})
	pool.createBrowserContext = func(context.Context, *rod.Browser, browserContextProxy) (proto.BrowserBrowserContextID, error) {
		return "execution-timeout-context", nil
	}
	pool.disposeBrowserContext = func(context.Context, *rod.Browser, proto.BrowserBrowserContextID) error { return nil }
	pool.captureBrowserContext = func(ctx context.Context, _ *rod.Browser, _ proto.BrowserBrowserContextID, _ string) ([]byte, error) {
		<-ctx.Done()
		close(cancelled)
		return nil, ctx.Err()
	}
	pool.current = &pooledBrowser{browser: &rod.Browser{}, stopped: make(chan struct{})}
	pool.generations[pool.current] = struct{}{}

	_, err := pool.Capture(context.Background(), "http://93.184.216.34/page")
	require.ErrorIs(t, err, context.DeadlineExceeded)
	awaitSignal(t, cancelled, "execution timeout did not cancel capture work")
}

func TestCapture_PageOpenFailureRetiresStaleGenerationAndRecovers(t *testing.T) {
	pool := NewPool()
	staleLauncher := &fakeBrowserLauncher{pid: 4254}
	stale := testGeneration(staleLauncher, 0)
	stale.retired = false
	pool.current = stale
	pool.generations[stale] = struct{}{}
	pool.closeBrowser = func(context.Context, *rod.Browser) error { return nil }
	pool.createBrowserContext = func(context.Context, *rod.Browser, browserContextProxy) (proto.BrowserBrowserContextID, error) {
		return "recovery-context", nil
	}
	pool.disposeBrowserContext = func(context.Context, *rod.Browser, proto.BrowserBrowserContextID) error { return nil }
	var captures atomic.Int64
	pool.captureBrowserContext = func(context.Context, *rod.Browser, proto.BrowserBrowserContextID, string) ([]byte, error) {
		if captures.Add(1) == 1 {
			return nil, fmt.Errorf("%w: stale browser", errPageOpen)
		}
		return []byte("png"), nil
	}
	freshLauncher := &fakeBrowserLauncher{url: "ws://127.0.0.1:9222/devtools/browser/fresh", pid: 4255}
	var launches atomic.Int64
	pool.newBrowserLauncher = func(string) browserLauncher {
		launches.Add(1)
		return freshLauncher
	}
	pool.connectBrowser = func(*rod.Browser) error { return nil }

	_, err := pool.Capture(context.Background(), "http://93.184.216.34/page")
	require.ErrorIs(t, err, errPageOpen)
	png, err := pool.Capture(context.Background(), "http://93.184.216.34/page")
	require.NoError(t, err)
	assert.Equal(t, []byte("png"), png)
	assert.Equal(t, int64(1), launches.Load(), "the next capture must launch a healthy generation")

	pool.Close()
	assert.Equal(t, int64(1), staleLauncher.cleanups.Load())
	assert.Equal(t, int64(1), freshLauncher.cleanups.Load())
}

func TestCleanupLauncherPassesBoundedContext(t *testing.T) {
	pool := NewPool()
	fake := &fakeBrowserLauncher{pid: 4248}
	fake.cleanup = func(ctx context.Context) error {
		deadline, ok := ctx.Deadline()
		assert.True(t, ok)
		assert.LessOrEqual(t, time.Until(deadline), pool.contextCleanupTimeout)
		return nil
	}

	require.NoError(t, pool.cleanupLauncher(fake))
	assert.Equal(t, int64(1), fake.cleanups.Load())
}

func TestCleanupFailureKillsThenRetries(t *testing.T) {
	pool := NewPool()
	fake := &fakeBrowserLauncher{pid: 4251}
	fake.cleanup = func(context.Context) error {
		if fake.cleanups.Load() == 1 {
			return errors.New("profile busy")
		}
		return nil
	}
	pool.closeBrowser = func(context.Context, *rod.Browser) error { return nil }
	pb := testGeneration(fake, 0)
	pool.generations[pb] = struct{}{}
	pool.stopBrowser(pb)
	assert.Equal(t, int64(1), fake.kills.Load())
	assert.Equal(t, int64(2), fake.cleanups.Load())
}

func TestRemoveAllContextHonorsCancellation(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.MkdirAll(dir+"/nested", 0o755))
	require.NoError(t, os.WriteFile(dir+"/nested/profile", []byte("state"), 0o600))
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	require.ErrorIs(t, removeAllContext(ctx, dir), context.Canceled)
	_, err := os.Stat(dir)
	require.NoError(t, err)
}

func TestRemoveAllContextDoesNotFollowSymlinkOutsideProfile(t *testing.T) {
	outside := t.TempDir()
	outsideFile := filepath.Join(outside, "keep")
	require.NoError(t, os.WriteFile(outsideFile, []byte("state"), 0o600))
	profile := filepath.Join(t.TempDir(), "profile")
	require.NoError(t, os.Mkdir(profile, 0o755))
	if err := os.Symlink(outside, filepath.Join(profile, "escape")); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}

	require.NoError(t, removeAllContext(context.Background(), profile))
	content, err := os.ReadFile(outsideFile)
	require.NoError(t, err)
	assert.Equal(t, []byte("state"), content)
}

func TestRetireBrowserDefersCloseWhileRefsHeld(t *testing.T) {
	pool := NewPool()
	var closed atomic.Int64
	started := make(chan struct{})
	release := make(chan struct{})
	pool.closeBrowser = func(context.Context, *rod.Browser) error {
		close(started)
		<-release
		closed.Add(1)
		return nil
	}
	pb := &pooledBrowser{browser: &rod.Browser{}, refs: 1, stopped: make(chan struct{})}
	pool.current = pb
	pool.generations[pb] = struct{}{}

	pool.retireBrowserGeneration(pb, false)
	assert.Zero(t, closed.Load())
	assert.Nil(t, pool.current)
	assert.True(t, pb.retired)
	pool.releaseBrowser(pb)
	awaitSignal(t, started, "retired generation teardown did not start")
	assert.Zero(t, closed.Load(), "release must not synchronously pay teardown latency")
	close(release)
	awaitSignal(t, pb.stopped, "retired generation teardown did not finish")
	assert.Equal(t, int64(1), closed.Load())
}

func TestCloseJoinsRetirementAlreadyInProgress(t *testing.T) {
	pool := NewPool()
	started := make(chan struct{})
	release := make(chan struct{})
	pool.closeBrowser = func(context.Context, *rod.Browser) error {
		close(started)
		<-release
		return nil
	}
	pb := testGeneration(&fakeBrowserLauncher{pid: 4257}, 1)
	pb.retired = false
	pool.current = pb
	pool.generations[pb] = struct{}{}

	pool.retireBrowserGeneration(pb, false)
	pool.releaseBrowser(pb)
	awaitSignal(t, started, "retired generation teardown did not start")
	closed := make(chan struct{})
	go func() {
		pool.Close()
		close(closed)
	}()
	select {
	case <-closed:
		t.Fatal("Close returned before in-progress retirement completed")
	case <-time.After(20 * time.Millisecond):
	}
	close(release)
	awaitSignal(t, closed, "Close did not join in-progress retirement")
	awaitSignal(t, pb.stopped, "retired generation did not signal teardown")
}

func TestConcurrentCaptureDuringResetClosesEachGenerationExactlyOnce(t *testing.T) {
	const (
		generationCount       = 4
		capturesPerGeneration = 8
	)
	type generationState struct {
		generation        *pooledBrowser
		resetObserved     chan struct{}
		active            int
		closes            int
		closedWhileActive bool
	}
	type acquisition struct {
		hold *pooledBrowser
		err  error
	}

	pool := NewPool()
	states := make(map[*rod.Browser]*generationState, generationCount)
	orderedStates := make([]*generationState, 0, generationCount)
	var stateMu sync.Mutex
	var unknownCloses int
	pool.closeBrowser = func(_ context.Context, browser *rod.Browser) error {
		stateMu.Lock()
		defer stateMu.Unlock()
		state := states[browser]
		if state == nil {
			unknownCloses++
			return nil
		}
		state.closes++
		state.closedWhileActive = state.closedWhileActive || state.active != 0
		return nil
	}

	finalRelease := make(chan struct{})
	var captures sync.WaitGroup
	for generationIndex := 0; generationIndex < generationCount; generationIndex++ {
		resetObserved := make(chan struct{})
		var resetOnce sync.Once
		generation := &pooledBrowser{
			browser: &rod.Browser{},
			cancel: func() {
				resetOnce.Do(func() { close(resetObserved) })
			},
			stopped: make(chan struct{}),
		}
		state := &generationState{generation: generation, resetObserved: resetObserved}
		stateMu.Lock()
		states[generation.browser] = state
		orderedStates = append(orderedStates, state)
		stateMu.Unlock()

		pool.mu.Lock()
		if pool.current != nil {
			pool.mu.Unlock()
			t.Fatalf("generation %d installed before the previous reset", generationIndex)
		}
		pool.current = generation
		pool.generations[generation] = struct{}{}
		pool.mu.Unlock()

		ready := make(chan struct{}, capturesPerGeneration)
		acquire := make(chan struct{})
		acquired := make(chan acquisition, capturesPerGeneration)
		captures.Add(capturesPerGeneration)
		for range capturesPerGeneration {
			go func() {
				defer captures.Done()
				ready <- struct{}{}
				<-acquire

				stateMu.Lock()
				_, hold, err := pool.acquireBrowser(context.Background())
				if err == nil {
					states[hold.browser].active++
				}
				stateMu.Unlock()
				acquired <- acquisition{hold: hold, err: err}
				if err != nil {
					return
				}

				<-finalRelease
				stateMu.Lock()
				states[hold.browser].active--
				pool.releaseBrowser(hold)
				stateMu.Unlock()
			}()
		}
		for range capturesPerGeneration {
			<-ready
		}
		close(acquire)
		for range capturesPerGeneration {
			result := <-acquired
			require.NoError(t, result.err)
			require.Same(t, generation, result.hold)
		}

		stateMu.Lock()
		assert.Equal(t, capturesPerGeneration, state.active)
		assert.Zero(t, state.closes, "generation %d closed before reset", generationIndex)
		stateMu.Unlock()
		if generationIndex == generationCount-1 {
			continue
		}

		retired := make(chan struct{})
		go func() {
			pool.retireBrowserGeneration(generation, false)
			close(retired)
		}()
		awaitSignal(t, retired, "generation reset did not finish")
		pool.mu.Lock()
		assert.Nil(t, pool.current)
		assert.True(t, generation.retired)
		assert.False(t, generation.stopping)
		assert.Equal(t, capturesPerGeneration, generation.refs)
		pool.mu.Unlock()
		stateMu.Lock()
		assert.Equal(t, capturesPerGeneration, state.active)
		assert.Zero(t, state.closes, "generation %d closed with captures still active", generationIndex)
		stateMu.Unlock()
	}

	finalState := orderedStates[len(orderedStates)-1]
	pool.mu.Lock()
	assert.Same(t, finalState.generation, pool.current)
	assert.False(t, finalState.generation.retired)
	pool.mu.Unlock()

	closeDone := make(chan struct{})
	go func() {
		pool.Close()
		close(closeDone)
	}()
	for _, state := range orderedStates {
		awaitSignal(t, state.resetObserved, "Close did not reset every generation")
	}
	select {
	case <-closeDone:
		t.Fatal("Close returned before active captures released their generations")
	default:
	}
	pool.mu.Lock()
	assert.Nil(t, pool.current)
	assert.True(t, finalState.generation.retired)
	assert.False(t, finalState.generation.stopping)
	assert.Equal(t, capturesPerGeneration, finalState.generation.refs)
	pool.mu.Unlock()
	stateMu.Lock()
	for generationIndex, state := range orderedStates {
		assert.Equal(t, capturesPerGeneration, state.active)
		assert.Zero(t, state.closes, "generation %d closed before final release", generationIndex)
	}
	stateMu.Unlock()

	close(finalRelease)
	captures.Wait()
	awaitSignal(t, closeDone, "Close did not join generation teardown")

	stateMu.Lock()
	defer stateMu.Unlock()
	assert.Zero(t, unknownCloses)
	for generationIndex, state := range orderedStates {
		assert.Zero(t, state.active, "generation %d retained active references", generationIndex)
		assert.False(t, state.closedWhileActive, "generation %d closed while references were active", generationIndex)
		assert.Equal(t, 1, state.closes, "generation %d must close exactly once", generationIndex)
	}
}

func TestCapture_LiveChrome(t *testing.T) {
	chrome := availableChrome(t)
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, "<html><body>capture</body></html>")
	}))
	t.Cleanup(origin.Close)
	t.Setenv("CHROME_PATH", chrome)
	pool := NewPool()
	t.Cleanup(pool.Close)
	pool.newCaptureProxy = func() (*captureProxy, error) {
		return newCaptureProxyWithDial(func(ctx context.Context, network, _ string) (net.Conn, error) {
			conn, err := (&net.Dialer{}).DialContext(ctx, network, origin.Listener.Addr().String())
			if err != nil {
				return nil, err
			}
			return &reportedRemoteConn{Conn: conn, addr: &net.TCPAddr{IP: net.ParseIP("93.184.216.34"), Port: 80}}, nil
		})
	}
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	_, port, err := net.SplitHostPort(origin.Listener.Addr().String())
	require.NoError(t, err)
	pageURL := "http://93.184.216.34:" + port

	png, err := pool.Capture(ctx, pageURL)
	require.NoError(t, err)
	require.NotEmpty(t, png)
	assert.Equal(t, []byte{0x89, 0x50, 0x4E, 0x47}, png[:4])
	png, err = pool.Capture(ctx, pageURL)
	require.NoError(t, err)
	require.NotEmpty(t, png)
}

func TestCapture_LiveChromeRoutesLoopbackThroughProxy(t *testing.T) {
	chrome := availableChrome(t)
	var hits atomic.Int64
	origin := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { hits.Add(1) }))
	t.Cleanup(origin.Close)
	t.Setenv("CHROME_PATH", chrome)
	pool := NewPool()
	processProxy, err := newCaptureProxy()
	require.NoError(t, err)
	browser, generation, err := pool.launchBrowser(context.Background(), wrapBrowserLauncher(configuredLauncherWithProxy(processProxy.Address())), nil)
	require.NoError(t, err)
	t.Cleanup(func() { pool.stopBrowser(generation) })
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	_, err = pool.captureWithBrowser(ctx, browser, processProxy, origin.URL)
	assert.True(t, processProxy.Blocked() || errors.Is(err, ErrEgressBlocked))
	assert.Zero(t, hits.Load())
}

func TestCapture_LiveChromeContextIsolation(t *testing.T) {
	chrome := availableChrome(t)
	img := image.NewRGBA(image.Rect(0, 0, 1, 1))
	img.Set(0, 0, color.White)
	var pixel bytes.Buffer
	require.NoError(t, png.Encode(&pixel, img))
	var subresourceHits atomic.Int64
	var leakedCookies atomic.Int64
	var publicOrigin string
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/start":
			http.Redirect(w, r, publicOrigin+"/page", http.StatusFound)
		case "/page":
			if _, err := r.Cookie("capture"); err == nil {
				leakedCookies.Add(1)
			}
			http.SetCookie(w, &http.Cookie{Name: "capture", Value: "one", Path: "/"})
			_, _ = w.Write([]byte(`<html><body><img src="/pixel.png"></body></html>`))
		case "/pixel.png":
			subresourceHits.Add(1)
			w.Header().Set("Cache-Control", "public, max-age=3600")
			w.Header().Set("Content-Type", "image/png")
			_, _ = w.Write(pixel.Bytes())
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(origin.Close)
	_, port, err := net.SplitHostPort(origin.Listener.Addr().String())
	require.NoError(t, err)
	publicOrigin = "http://93.184.216.34:" + port

	t.Setenv("CHROME_PATH", chrome)
	pool := NewPool()
	t.Cleanup(pool.Close)
	pool.newCaptureProxy = func() (*captureProxy, error) {
		return newCaptureProxyWithDial(func(ctx context.Context, network, _ string) (net.Conn, error) {
			conn, dialErr := (&net.Dialer{}).DialContext(ctx, network, origin.Listener.Addr().String())
			if dialErr != nil {
				return nil, dialErr
			}
			return &reportedRemoteConn{Conn: conn, addr: &net.TCPAddr{IP: net.ParseIP("93.184.216.34"), Port: 80}}, nil
		})
	}
	testCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	for range 2 {
		pngBytes, err := pool.Capture(testCtx, publicOrigin+"/start")
		require.NoError(t, err)
		require.NotEmpty(t, pngBytes)
	}
	assert.Zero(t, leakedCookies.Load(), "cookies must not cross real Pool.Capture calls")
	assert.Equal(t, int64(2), subresourceHits.Load(), "cache and subresources must be isolated per capture")
}

func TestCapture_LiveChromeRequestBudgetCountsTunnelTraffic(t *testing.T) {
	chrome := availableChrome(t)
	var publicOrigin string
	var originRequests atomic.Int64
	var h2Requests atomic.Int64
	origin := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		originRequests.Add(1)
		if r.ProtoMajor == 2 {
			h2Requests.Add(1)
		}
		if r.URL.Path == "/page" {
			_, _ = w.Write([]byte(`<html><body><img src="/one"><img src="/two"></body></html>`))
			return
		}
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write([]byte{0x89, 0x50, 0x4e, 0x47})
	}))
	origin.EnableHTTP2 = true
	origin.StartTLS()
	t.Cleanup(origin.Close)
	_, port, err := net.SplitHostPort(origin.Listener.Addr().String())
	require.NoError(t, err)
	publicOrigin = "https://93.184.216.34:" + port

	t.Setenv("CHROME_PATH", chrome)
	pool := NewPool()
	pool.requestLimit = 2
	pool.newBrowserLauncher = func(proxyAddress string) browserLauncher {
		return wrapBrowserLauncher(configuredLauncherWithProxy(proxyAddress).Set("ignore-certificate-errors"))
	}
	var proxies []*captureProxy
	pool.newCaptureProxy = func() (*captureProxy, error) {
		proxy, proxyErr := newCaptureProxyWithDial(func(ctx context.Context, network, addr string) (net.Conn, error) {
			if addr != publicOrigin[len("https://"):] {
				return nil, fmt.Errorf("unexpected test target %s", addr)
			}
			conn, dialErr := (&net.Dialer{}).DialContext(ctx, network, origin.Listener.Addr().String())
			if dialErr != nil {
				return nil, dialErr
			}
			return &reportedRemoteConn{Conn: conn, addr: &net.TCPAddr{IP: net.ParseIP("93.184.216.34"), Port: 443}}, nil
		})
		if proxyErr == nil {
			proxies = append(proxies, proxy)
		}
		return proxy, proxyErr
	}
	t.Cleanup(pool.Close)

	_, err = pool.Capture(context.Background(), publicOrigin+"/page")
	require.ErrorIs(t, err, ErrEgressBlocked)
	require.ErrorIs(t, err, errProxyBudgetExceeded)
	assert.GreaterOrEqual(t, originRequests.Load(), pool.requestLimit, "the browser must consume the budget over one HTTPS tunnel")
	assert.Equal(t, originRequests.Load(), h2Requests.Load(), "all counted origin traffic must be HTTP/2 inside CONNECT")
	assert.Condition(t, func() bool {
		for _, proxy := range proxies {
			if proxy.requests.Load() > 0 {
				return true
			}
		}
		return false
	}, "the HTTPS origin must be reached through a capture proxy CONNECT")
}

func availableChrome(t *testing.T) string {
	t.Helper()
	if chrome := os.Getenv("CHROME_PATH"); chrome != "" {
		if _, err := os.Stat(chrome); err == nil {
			return chrome
		}
		t.Skipf("CHROME_PATH browser is absent: %s", chrome)
	}
	for _, candidate := range []string{
		"/Applications/Google Chrome.app/Contents/MacOS/Google Chrome",
		"/Applications/Chromium.app/Contents/MacOS/Chromium",
		"/usr/bin/chromium-browser", "/usr/bin/chromium", "/usr/bin/google-chrome",
	} {
		if _, err := os.Stat(candidate); err == nil {
			return candidate
		}
	}
	t.Skip("no Chrome or Chromium binary available")
	return ""
}

func testGeneration(fake browserLauncher, refs int) *pooledBrowser {
	return &pooledBrowser{
		browser: &rod.Browser{}, launcher: fake, pid: fake.PID(), refs: refs,
		stopped: make(chan struct{}), retired: true,
	}
}

func boolInt(value bool) int {
	if value {
		return 1
	}
	return 0
}

func awaitSignal(t *testing.T, signal <-chan struct{}, failure string) {
	t.Helper()
	select {
	case <-signal:
	case <-time.After(250 * time.Millisecond):
		t.Fatal(failure)
	}
}
