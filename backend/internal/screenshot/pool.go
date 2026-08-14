package screenshot

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"sync"
	"time"

	"github.com/go-rod/rod"
	"github.com/go-rod/rod/lib/launcher"
	"github.com/go-rod/rod/lib/launcher/flags"
	"github.com/go-rod/rod/lib/proto"
)

const (
	defaultCaptureQueueTimeout     = 5 * time.Second
	defaultBrowserStartupTimeout   = 30 * time.Second
	defaultCaptureExecutionTimeout = 30 * time.Second
	defaultContextCleanupTimeout   = 2 * time.Second
	defaultBrowserShutdownTimeout  = 7 * time.Second
)

var errPoolClosed = errors.New("screenshot: browser pool closed")

// Pool owns one lazily started Chromium process and its lifecycle state.
// Captures are serialized because the process proxy's blocked signal cannot be
// attributed safely between concurrent pages.
type Pool struct {
	mu          sync.Mutex
	current     *pooledBrowser
	starting    *browserStartup
	generations map[*pooledBrowser]struct{}
	epoch       uint64
	closed      bool
	shutdown    chan struct{}
	closeDone   chan struct{}
	captureSem  chan struct{}

	queueTimeout          time.Duration
	startupTimeout        time.Duration
	executionTimeout      time.Duration
	contextCleanupTimeout time.Duration
	closeTimeout          time.Duration
	shutdownTimeout       time.Duration
	requestLimit          int64

	createBrowserContext  func(context.Context, *rod.Browser, browserContextProxy) (proto.BrowserBrowserContextID, error)
	disposeBrowserContext func(context.Context, *rod.Browser, proto.BrowserBrowserContextID) error
	captureBrowserContext func(context.Context, *rod.Browser, proto.BrowserBrowserContextID, string) ([]byte, error)
	closeBrowser          func(context.Context, *rod.Browser) error
	connectBrowser        func(*rod.Browser) error
	newBrowserLauncher    func(string) browserLauncher
	newCaptureProxy       func() (*captureProxy, error)
}

// NewPool creates an idle pool. Chromium starts on the first Capture.
func NewPool() *Pool {
	p := &Pool{
		generations:           make(map[*pooledBrowser]struct{}),
		shutdown:              make(chan struct{}),
		closeDone:             make(chan struct{}),
		captureSem:            make(chan struct{}, 1),
		queueTimeout:          defaultCaptureQueueTimeout,
		startupTimeout:        defaultBrowserStartupTimeout,
		executionTimeout:      defaultCaptureExecutionTimeout,
		contextCleanupTimeout: defaultContextCleanupTimeout,
		closeTimeout:          defaultContextCleanupTimeout,
		shutdownTimeout:       defaultBrowserShutdownTimeout,
		requestLimit:          maxProxyRequests,
		connectBrowser:        func(browser *rod.Browser) error { return browser.Connect() },
		newCaptureProxy:       newCaptureProxy,
	}
	p.captureBrowserContext = func(ctx context.Context, browser *rod.Browser, contextID proto.BrowserBrowserContextID, pageURL string) ([]byte, error) {
		return capturePage(ctx, browser, contextID, pageURL, p.requestLimit)
	}
	p.createBrowserContext = func(ctx context.Context, browser *rod.Browser, proxy browserContextProxy) (proto.BrowserBrowserContextID, error) {
		result, err := (proto.TargetCreateBrowserContext{
			DisposeOnDetach: true,
			ProxyServer:     proxy.Server,
			ProxyBypassList: proxy.BypassList,
		}).Call(browser.Context(ctx))
		if err != nil {
			return "", err
		}
		return result.BrowserContextID, nil
	}
	p.disposeBrowserContext = func(ctx context.Context, browser *rod.Browser, id proto.BrowserBrowserContextID) error {
		return (proto.TargetDisposeBrowserContext{BrowserContextID: id}).Call(browser.Context(ctx))
	}
	p.closeBrowser = func(ctx context.Context, browser *rod.Browser) error {
		if browser == nil {
			return nil
		}
		return browser.Context(ctx).Close()
	}
	p.newBrowserLauncher = func(proxyAddress string) browserLauncher {
		return wrapBrowserLauncher(configuredLauncherWithProxy(proxyAddress))
	}
	return p
}

type pooledBrowser struct {
	browser   *rod.Browser
	launcher  browserLauncher
	proxy     *captureProxy
	cancel    context.CancelFunc
	stopped   chan struct{}
	pid       int
	refs      int
	retired   bool
	forceStop bool
	stopping  bool

	launcherCleanupOnce sync.Once
	finishStopOnce      sync.Once
}

type browserStartup struct {
	done         chan struct{}
	cancel       context.CancelFunc
	err          error
	launcher     browserLauncher
	proxy        *captureProxy
	hardStopOnce sync.Once
}

type browserLauncher interface {
	Launch(context.Context) (string, error)
	PID() int
	Kill()
	Cleanup(context.Context) error
}

type rodBrowserLauncher struct {
	*launcher.Launcher
	userDataDir string
}

func (l *rodBrowserLauncher) Launch(ctx context.Context) (string, error) {
	return l.Launcher.Context(ctx).Launch()
}

func (l *rodBrowserLauncher) Cleanup(ctx context.Context) error {
	return removeAllContext(ctx, l.userDataDir)
}

func wrapBrowserLauncher(l *launcher.Launcher) browserLauncher {
	return &rodBrowserLauncher{Launcher: l, userDataDir: l.Get(flags.UserDataDir)}
}

func configuredLauncherWithProxy(proxyAddress string) *launcher.Launcher {
	l := launcher.New().
		Leakless(false).
		Set("disable-gpu").
		Set("disable-dev-shm-usage").
		Set("disable-quic").
		Set("disable-webrtc-multiple-routes").
		Set("force-webrtc-ip-handling-policy", "disable_non_proxied_udp").
		Headless(true)
	if proxyAddress != "" {
		l = l.Set("proxy-server", proxyAddress).
			Set("proxy-bypass-list", proxyBypassNone)
	}
	if path := os.Getenv("CHROME_PATH"); path != "" {
		l = l.Bin(path)
	}
	return l
}

func (p *Pool) launchBrowser(ctx context.Context, l browserLauncher, proxy *captureProxy) (*rod.Browser, *pooledBrowser, error) {
	return p.launchBrowserWithCleanup(ctx, l, proxy, func() { p.hardStopLauncher(l) })
}

func (p *Pool) launchBrowserWithCleanup(ctx context.Context, l browserLauncher, proxy *captureProxy, cleanup func()) (*rod.Browser, *pooledBrowser, error) {
	browserCtx, browserCancel := context.WithCancel(context.Background())
	stopStartupCancel := context.AfterFunc(ctx, browserCancel)
	controlURL, err := l.Launch(browserCtx)
	if err != nil {
		stopStartupCancel()
		browserCancel()
		cleanup()
		if ctx.Err() != nil {
			err = ctx.Err()
		}
		return nil, nil, fmt.Errorf("screenshot: launch browser: %w", err)
	}
	browser := rod.New().Context(browserCtx).ControlURL(controlURL)
	if err := p.connectBrowser(browser); err != nil {
		stopStartupCancel()
		browserCancel()
		cleanup()
		if ctx.Err() != nil {
			err = ctx.Err()
		}
		return nil, nil, fmt.Errorf("screenshot: connect browser: %w", err)
	}
	if !stopStartupCancel() || ctx.Err() != nil {
		browserCancel()
		cleanup()
		return nil, nil, fmt.Errorf("screenshot: start browser: %w", ctx.Err())
	}
	return browser, &pooledBrowser{
		browser: browser, launcher: l, proxy: proxy, cancel: browserCancel,
		stopped: make(chan struct{}), pid: l.PID(), refs: 1,
	}, nil
}

// acquireBrowser launches outside the mutex and gives startup/connect a budget
// independent from queue and page execution.
func (p *Pool) acquireBrowser(ctx context.Context) (*rod.Browser, *pooledBrowser, error) {
	waitCtx, waitCancel := context.WithTimeout(ctx, p.startupTimeout)
	defer waitCancel()

	for {
		p.mu.Lock()
		if p.closed {
			p.mu.Unlock()
			return nil, nil, errPoolClosed
		}
		if p.current != nil {
			p.current.refs++
			browser, hold := p.current.browser, p.current
			p.mu.Unlock()
			return browser, hold, nil
		}
		if p.starting != nil {
			startup := p.starting
			p.mu.Unlock()
			select {
			case <-startup.done:
				if startup.err != nil {
					return nil, nil, startup.err
				}
				continue
			case <-waitCtx.Done():
				return nil, nil, fmt.Errorf("screenshot: wait for browser startup: %w", waitCtx.Err())
			}
		}

		startupCtx, startupCancel := context.WithCancel(waitCtx)
		startup := &browserStartup{done: make(chan struct{}), cancel: startupCancel}
		p.starting = startup
		epoch := p.epoch
		p.mu.Unlock()

		browser, pooled, err := p.startBrowserGeneration(startupCtx, startup)
		startupCancel()

		p.mu.Lock()
		stale := p.closed || epoch != p.epoch || p.current != nil
		if err == nil && !stale {
			p.current = pooled
			p.generations[pooled] = struct{}{}
			startup.launcher = nil
			startup.proxy = nil
		} else if err == nil {
			pooled.refs = 0
			pooled.retired = true
			pooled.stopping = true
			err = fmt.Errorf("screenshot: browser startup retired")
		}
		startup.err = err
		p.starting = nil
		p.mu.Unlock()

		if stale && pooled != nil {
			p.stopBrowser(pooled)
		}
		// Stale generation teardown finishes before startup waiters are released.
		close(startup.done)
		if err != nil {
			return nil, nil, err
		}
		return browser, pooled, nil
	}
}

func (p *Pool) startBrowserGeneration(ctx context.Context, startup *browserStartup) (*rod.Browser, *pooledBrowser, error) {
	proxy, err := p.newCaptureProxy()
	if err != nil {
		return nil, nil, err
	}
	launcher := p.newBrowserLauncher(proxy.Address())
	p.mu.Lock()
	startup.launcher = launcher
	startup.proxy = proxy
	p.mu.Unlock()
	if ctx.Err() != nil {
		p.hardStopStartup(startup)
		return nil, nil, fmt.Errorf("screenshot: start browser: %w", ctx.Err())
	}
	return p.launchBrowserWithCleanup(ctx, launcher, proxy, func() { p.hardStopStartup(startup) })
}

func (p *Pool) hardStopStartup(startup *browserStartup) {
	if startup == nil {
		return
	}
	startup.hardStopOnce.Do(func() {
		p.mu.Lock()
		launcher, proxy := startup.launcher, startup.proxy
		p.mu.Unlock()
		p.hardStopLauncher(launcher)
		if proxy != nil {
			proxy.Close()
		}
	})
}

func (p *Pool) releaseBrowser(pb *pooledBrowser) {
	if pb == nil {
		return
	}
	p.mu.Lock()
	if pb.refs > 0 {
		pb.refs--
	}
	stop := p.markBrowserStoppingLocked(pb)
	p.mu.Unlock()
	if stop {
		go p.stopBrowser(pb)
	}
}

func (p *Pool) retireBrowserGeneration(pb *pooledBrowser, force bool) {
	if pb == nil {
		return
	}
	p.mu.Lock()
	if p.current == pb {
		p.current = nil
	}
	pb.retired = true
	pb.forceStop = pb.forceStop || force
	stop := p.markBrowserStoppingLocked(pb)
	p.mu.Unlock()
	if stop {
		go p.stopBrowser(pb)
	}
}

func (p *Pool) markBrowserStoppingLocked(pb *pooledBrowser) bool {
	if pb == nil || !pb.retired || pb.refs != 0 || pb.stopping {
		return false
	}
	pb.stopping = true
	return true
}

func (p *Pool) stopBrowser(pb *pooledBrowser) {
	if pb.cancel != nil {
		pb.cancel()
	}
	if pb.forceStop {
		p.cleanupGenerationLauncher(pb, true)
	} else {
		ctx, cancel := context.WithTimeout(context.Background(), p.closeTimeout)
		err := p.closeBrowser(ctx, pb.browser)
		cancel()
		if err != nil {
			slog.Warn("screenshot: pooled browser close failed; forcing process stop", "pid", pb.pid, "err", err)
			p.cleanupGenerationLauncher(pb, true)
		} else {
			p.cleanupGenerationLauncher(pb, false)
		}
	}
	p.finishBrowserStop(pb)
}

func (p *Pool) cleanupGenerationLauncher(pb *pooledBrowser, force bool) {
	pb.launcherCleanupOnce.Do(func() {
		if pb.launcher == nil {
			return
		}
		if force {
			p.hardStopLauncher(pb.launcher)
			return
		}
		if err := p.cleanupLauncher(pb.launcher); err != nil {
			killLauncher(pb.launcher)
			if retryErr := p.cleanupLauncher(pb.launcher); retryErr != nil {
				logLauncherCleanupError(pb.launcher, errors.Join(err, retryErr))
			}
		}
	})
}

func (p *Pool) forceBrowserStop(pb *pooledBrowser) {
	if pb.cancel != nil {
		pb.cancel()
	}
	p.mu.Lock()
	pb.stopping = true
	p.mu.Unlock()
	p.cleanupGenerationLauncher(pb, true)
	p.finishBrowserStop(pb, false)
}

func (p *Pool) finishBrowserStop(pb *pooledBrowser, clearBrowser ...bool) {
	pb.finishStopOnce.Do(func() {
		if pb.proxy != nil {
			pb.proxy.Close()
		}
		p.mu.Lock()
		if len(clearBrowser) == 0 || clearBrowser[0] {
			pb.browser = nil
		}
		delete(p.generations, pb)
		stopped := pb.stopped
		p.mu.Unlock()
		if stopped != nil {
			close(stopped)
		}
	})
}

func (p *Pool) hardStopLauncher(l browserLauncher) {
	if l == nil {
		return
	}
	killLauncher(l)
	if err := p.cleanupLauncher(l); err != nil {
		if retryErr := p.cleanupLauncher(l); retryErr != nil {
			logLauncherCleanupError(l, errors.Join(err, retryErr))
		}
	}
}

func killLauncher(l browserLauncher) {
	if l.PID() != 0 {
		l.Kill()
	}
}

func (p *Pool) cleanupLauncher(l browserLauncher) error {
	ctx, cancel := context.WithTimeout(context.Background(), p.contextCleanupTimeout)
	defer cancel()
	return l.Cleanup(ctx)
}

func logLauncherCleanupError(l browserLauncher, err error) {
	slog.Warn("screenshot: launcher cleanup failed", "pid", l.PID(), "err", err)
}

// Close prevents new captures, cancels active work, and waits up to the
// shutdown budget for every known process and profile teardown.
func (p *Pool) Close() {
	if p == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), p.shutdownTimeout)
	defer cancel()

	p.mu.Lock()
	if p.closed {
		done := p.closeDone
		p.mu.Unlock()
		waitForTeardown(ctx, done)
		return
	}
	p.closed = true
	close(p.shutdown)
	p.epoch++
	startup := p.starting
	if startup != nil {
		startup.cancel()
	}
	p.current = nil
	generations := make([]*pooledBrowser, 0, len(p.generations))
	toStop := make([]*pooledBrowser, 0, len(p.generations))
	for generation := range p.generations {
		generation.retired = true
		generations = append(generations, generation)
		if p.markBrowserStoppingLocked(generation) {
			toStop = append(toStop, generation)
		}
	}
	p.mu.Unlock()
	defer close(p.closeDone)
	if startup != nil {
		p.hardStopStartup(startup)
	}

	for _, generation := range generations {
		if generation.cancel != nil {
			generation.cancel()
		}
	}
	for _, generation := range toStop {
		go p.stopBrowser(generation)
	}

	if startup != nil && !waitForTeardown(ctx, startup.done) {
		slog.Warn("screenshot: browser startup teardown exceeded shutdown timeout", "err", ctx.Err())
	}
	for _, generation := range generations {
		if !waitForTeardown(ctx, generation.stopped) {
			slog.Warn("screenshot: forcing browser generation after shutdown grace", "pid", generation.pid, "err", ctx.Err())
			p.forceBrowserStop(generation)
		}
	}
}

func waitForTeardown(ctx context.Context, done <-chan struct{}) bool {
	if done == nil {
		return true
	}
	select {
	case <-done:
		return true
	case <-ctx.Done():
		return false
	}
}
