package server

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"foldex/internal/abusepolicy"
	"foldex/internal/backup"
	"foldex/internal/config"
	"foldex/internal/links"
	"foldex/internal/pkg/authctx"
)

// fixedPolicy is the live-policy seam with the reload taken out, so a test can
// state the numbers it is asserting on instead of standing up a database.
type fixedPolicy struct{ p abusepolicy.Policy }

func (f fixedPolicy) Current(context.Context) abusepolicy.Policy { return f.p }

func policyWith(writes, expensive int) fixedPolicy {
	p := abusepolicy.Default()
	p.APIWritesPerMinute = writes
	p.APIExpensivePerHour = expensive
	return fixedPolicy{p: p}
}

// quotaHarness is the middleware under test with a principal already resolved
// above it, which is exactly where router.go mounts it.
func quotaHarness(pol policyReader, principal authctx.Principal) (http.Handler, *atomic.Int64) {
	var reached atomic.Int64
	q := newAPIQuota(pol, nil)
	h := q.middleware(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		reached.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h.ServeHTTP(w, r.WithContext(authctx.WithPrincipal(r.Context(), principal)))
	}), &reached
}

func editor(id int64) authctx.Principal {
	return authctx.Principal{UserID: authctx.UserID(id), Role: authctx.RoleEditor, SessionID: 1, Via: authctx.ViaSession}
}

func hit(h http.Handler, method, path string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(method, path, nil))
	return rec
}

// 1. Inside the quota the request is served; past it the answer is 429 with a
// Retry-After the client can actually act on.
func TestAPIQuota_RefusesPastTheBudgetWithAPlausibleRetryAfter(t *testing.T) {
	t.Parallel()
	h, reached := quotaHarness(policyWith(3, 20), editor(7))

	for i := 0; i < 3; i++ {
		require.Equal(t, http.StatusOK, hit(h, http.MethodPost, "/api/links").Code, "write %d", i+1)
	}

	rec := hit(h, http.MethodPost, "/api/links")
	require.Equal(t, http.StatusTooManyRequests, rec.Code)
	assert.EqualValues(t, 3, reached.Load(), "a refused request must not reach the handler")

	// The envelope is the uniform one, not a bare string: the SPA renders
	// error.message and would show nothing for a plain-text body.
	var body struct {
		Error struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	assert.Equal(t, "rate_limited", body.Error.Code)
	assert.NotEmpty(t, body.Error.Message)

	ra := rec.Header().Get("Retry-After")
	require.NotEmpty(t, ra, "429 without Retry-After leaves a well-behaved client guessing")
	secs, err := strconv.Atoi(ra)
	require.NoError(t, err, "Retry-After must be delta-seconds")
	assert.GreaterOrEqual(t, secs, 1)
	assert.LessOrEqual(t, secs, 3600)
}

// 2. Reading is cheap and writing costs; a GET flood is a different layer's
// problem and must not spend the write budget of the person browsing.
func TestAPIQuota_ReadsNeverConsumeTheWriteBudget(t *testing.T) {
	t.Parallel()
	h, _ := quotaHarness(policyWith(2, 20), editor(7))

	for i := 0; i < 50; i++ {
		require.Equal(t, http.StatusOK, hit(h, http.MethodGet, "/api/links").Code)
	}
	require.Equal(t, http.StatusOK, hit(h, http.MethodHead, "/api/links").Code)
	require.Equal(t, http.StatusOK, hit(h, http.MethodOptions, "/api/links").Code)

	assert.Equal(t, http.StatusOK, hit(h, http.MethodPost, "/api/links").Code)
	assert.Equal(t, http.StatusOK, hit(h, http.MethodPost, "/api/links").Code)
	assert.Equal(t, http.StatusTooManyRequests, hit(h, http.MethodPost, "/api/links").Code,
		"the write budget must still be exactly 2 after fifty reads")
}

// Every mutating verb costs, not just POST. A quota that only counted POST
// would be bypassed by a loop of PATCHes.
func TestAPIQuota_EveryMutatingVerbCosts(t *testing.T) {
	t.Parallel()
	for _, m := range []string{http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete} {
		h, _ := quotaHarness(policyWith(1, 20), editor(7))
		require.Equal(t, http.StatusOK, hit(h, m, "/api/links/1").Code, m)
		assert.Equal(t, http.StatusTooManyRequests, hit(h, m, "/api/links/1").Code, m)
	}
}

// 3. An expensive route pays both budgets, and the smaller one is what bites.
func TestAPIQuota_AnExpensiveRouteChargesBothBucketsAndTheSmallerBitesFirst(t *testing.T) {
	t.Parallel()
	h, _ := quotaHarness(policyWith(100, 3), editor(7))

	for i := 0; i < 3; i++ {
		require.Equal(t, http.StatusOK, hit(h, http.MethodPost, "/api/links/9/screenshot").Code, "capture %d", i+1)
	}
	assert.Equal(t, http.StatusTooManyRequests, hit(h, http.MethodPost, "/api/links/9/screenshot").Code,
		"the hourly ceiling of 3 must bite long before the per-minute one of 100")

	// It charged the ordinary bucket too: three captures spent three of the
	// hundred writes, and the refusal spent none of them.
	for i := 0; i < 97; i++ {
		require.Equal(t, http.StatusOK, hit(h, http.MethodPost, "/api/links").Code, "ordinary write %d", i+1)
	}
	assert.Equal(t, http.StatusTooManyRequests, hit(h, http.MethodPost, "/api/links").Code,
		"an expensive route must also consume the ordinary write budget")
}

// A request the write budget refuses must not silently burn an expensive token
// on work that never ran — that is how hitting one ceiling drags the other one
// down with it, and how a user who hit the import ceiling finds their ordinary
// writes throttled too.
func TestAPIQuota_ARefusedRequestCostsNoBudgetAtAll(t *testing.T) {
	t.Parallel()
	pol := &mutablePolicy{p: abusepolicy.Default()}
	pol.set(1, 5)

	c := newTestClock()
	q := newAPIQuota(pol, nil)
	q.writes.WithClock(c.now)
	q.expensive.WithClock(c.now)

	served := 0
	h := q.middleware(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		served++
		w.WriteHeader(http.StatusOK)
	}))
	serve := func(path string) int {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, path, nil)
		h.ServeHTTP(rec, req.WithContext(authctx.WithPrincipal(req.Context(), editor(7))))
		return rec.Code
	}

	require.Equal(t, http.StatusOK, serve("/api/links"))
	// The write bucket is now empty. These are refused THERE — after the
	// expensive bucket has already been asked, which is the moment a naive
	// implementation loses the token.
	for i := 0; i < 4; i++ {
		require.Equal(t, http.StatusTooManyRequests, serve("/api/import/apply"))
	}

	// Take the write ceiling out of the way and count what the expensive
	// bucket still admits. All five must be there.
	pol.set(1000, 5)
	c.advance(time.Minute)
	served = 0
	for i := 0; i < 5; i++ {
		assert.Equalf(t, http.StatusOK, serve("/api/import/apply"),
			"expensive request %d was refused — a refusal burned its budget", i+1)
	}
	assert.Equal(t, http.StatusTooManyRequests, serve("/api/import/apply"))
	assert.Equal(t, 5, served)
}

// testClock is a manually advanced time source, so a test can cross a refill
// window without sleeping through one.
type testClock struct {
	mu sync.Mutex
	t  time.Time
}

func newTestClock() *testClock { return &testClock{t: time.Unix(1_700_000_000, 0)} }

func (c *testClock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *testClock) advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

// 4. Two principals must never share a budget. SDD §8 names this as the
// revert criterion: a limiter that locks out a user who did nothing denies
// service for free and teaches the operator to switch it off.
func TestAPIQuota_TwoPrincipalsHaveIndependentBudgets(t *testing.T) {
	t.Parallel()
	pol := policyWith(2, 20)
	q := newAPIQuota(pol, nil)
	serve := func(p authctx.Principal, method, path string) int {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(method, path, nil)
		q.middleware(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusOK)
		})).ServeHTTP(rec, req.WithContext(authctx.WithPrincipal(req.Context(), p)))
		return rec.Code
	}

	require.Equal(t, http.StatusOK, serve(editor(1), http.MethodPost, "/api/links"))
	require.Equal(t, http.StatusOK, serve(editor(1), http.MethodPost, "/api/links"))
	require.Equal(t, http.StatusTooManyRequests, serve(editor(1), http.MethodPost, "/api/links"))

	assert.Equal(t, http.StatusOK, serve(editor(2), http.MethodPost, "/api/links"),
		"user 2 must not pay for user 1's traffic")
	assert.Equal(t, http.StatusOK, serve(editor(2), http.MethodPost, "/api/links"))
	assert.Equal(t, http.StatusTooManyRequests, serve(editor(2), http.MethodPost, "/api/links"))
}

// 5. The owner is NOT exempt. An exemption would be an account able to take the
// instance down, and the first person to trip over it would be the operator
// running a large import.
func TestAPIQuota_TheOwnerIsNotExempt(t *testing.T) {
	t.Parallel()
	for _, role := range []authctx.Role{authctx.RoleOwner, authctx.RoleAdmin, authctx.RoleEditor, authctx.RoleViewer} {
		h, _ := quotaHarness(policyWith(1, 20), authctx.Principal{
			UserID: 42, Role: role, SessionID: 1, Via: authctx.ViaSession,
		})
		require.Equal(t, http.StatusOK, hit(h, http.MethodPost, "/api/links").Code, string(role))
		assert.Equal(t, http.StatusTooManyRequests, hit(h, http.MethodPost, "/api/links").Code,
			"role %q was let past its own quota", role)
	}
}

// An API token is a credential of an ACCOUNT, not a second account. Keying the
// bucket on the token would make minting one a way to multiply the budget.
func TestAPIQuota_AnAPITokenShareTheAccountsBudget(t *testing.T) {
	t.Parallel()
	pol := policyWith(2, 20)
	q := newAPIQuota(pol, nil)
	serve := func(p authctx.Principal) int {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/api/links", nil)
		q.middleware(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusOK)
		})).ServeHTTP(rec, req.WithContext(authctx.WithPrincipal(req.Context(), p)))
		return rec.Code
	}

	session := authctx.Principal{UserID: 9, Role: authctx.RoleEditor, SessionID: 3, Via: authctx.ViaSession}
	token := authctx.Principal{UserID: 9, Role: authctx.RoleEditor, TokenID: 4, Via: authctx.ViaAPIToken}

	require.Equal(t, http.StatusOK, serve(session))
	require.Equal(t, http.StatusOK, serve(token))
	assert.Equal(t, http.StatusTooManyRequests, serve(token),
		"a bearer token must not be a second budget for the same account")
}

// 6. The cap has to hold when the requests arrive at once, which is how they
// arrive in production. Run with -race.
func TestAPIQuota_ConcurrentRequestsCannotExceedTheBudget(t *testing.T) {
	t.Parallel()
	const limit = 20
	h, reached := quotaHarness(policyWith(limit, 1000), editor(7))

	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := 0; i < 200; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			hit(h, http.MethodPost, "/api/links")
		}()
	}
	close(start)
	wg.Wait()

	assert.EqualValues(t, limit, reached.Load(),
		"200 parallel writes were served %d times against a budget of %d", reached.Load(), limit)
}

// The live policy is read per request, so an owner tightening a limit does not
// have to restart the instance being defended.
func TestAPIQuota_ANewLimitTakesEffectWithoutARestart(t *testing.T) {
	t.Parallel()
	pol := &mutablePolicy{p: abusepolicy.Default()}
	pol.set(50, 20)
	q := newAPIQuota(pol, nil)
	serve := func() int {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/api/links", nil)
		q.middleware(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusOK)
		})).ServeHTTP(rec, req.WithContext(authctx.WithPrincipal(req.Context(), editor(7))))
		return rec.Code
	}

	require.Equal(t, http.StatusOK, serve())
	// 49 of the old budget of 50 are still unspent. Tightening to 1 must clamp
	// the bucket down to the NEW capacity rather than let the caller keep
	// spending the headroom the old limit gave them.
	pol.set(1, 20)
	assert.Equal(t, http.StatusOK, serve(), "the new budget of 1 must still admit one request")
	assert.Equal(t, http.StatusTooManyRequests, serve(),
		"a tightened limit must bind on the next request, not on the next boot")
}

type mutablePolicy struct {
	mu sync.Mutex
	p  abusepolicy.Policy
}

func (m *mutablePolicy) set(writes, expensive int) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.p.APIWritesPerMinute = writes
	m.p.APIExpensivePerHour = expensive
}

func (m *mutablePolicy) Current(context.Context) abusepolicy.Policy {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.p
}

// An unwired policy must resolve to the compiled defaults rather than to "no
// limit" — a dependency that silently switches a defence off is the failure
// mode INV-177 was written about.
//
// There are TWO nils here and they are answered by different code, which is
// worth spelling out because one of them looks like it covers the other and
// does not. `Deps.AbusePolicy == nil` is a TYPED nil in a non-nil interface, so
// apiQuota.current's own `q.pol == nil` never fires for it; what answers is
// Cache.Current's nil-receiver guard. A caller that passes no reader at all —
// an untyped nil interface — is the only thing that reaches the local check.
// Assert both, or deleting the one that actually works looks safe.
func TestAPIQuota_ANilPolicySourceEnforcesTheCompiledDefaults(t *testing.T) {
	t.Parallel()
	want := abusepolicy.Default()

	t.Run("typed nil cache, the shape router.go actually passes", func(t *testing.T) {
		var cache *abusepolicy.Cache
		q := newAPIQuota(cache, nil)
		// A plain Go comparison, not require.NotNil: testify reflects INTO the
		// interface and calls a nil pointer nil, which is the opposite of what
		// the language does here — and the language is what `q.pol == nil` in
		// the production code is evaluated by.
		require.True(t, q.pol != nil, "a typed nil in an interface is not a nil interface")
		assert.Equal(t, want.APIWritesPerMinute, q.writeLimit(context.Background()))
		assert.Equal(t, want.APIExpensivePerHour, q.expensiveLimit(context.Background()))
	})

	t.Run("no reader at all", func(t *testing.T) {
		var absent policyReader
		q := newAPIQuota(absent, nil)
		require.True(t, q.pol == nil)
		assert.Equal(t, want.APIWritesPerMinute, q.writeLimit(context.Background()))
		assert.Equal(t, want.APIExpensivePerHour, q.expensiveLimit(context.Background()))
	})

	// And the enforcement is real, not just the number: an unwired quota still
	// refuses.
	t.Run("still refuses past the compiled budget", func(t *testing.T) {
		var absent policyReader
		q := newAPIQuota(absent, nil)
		q.writes.WithClock(newTestClock().now) // freeze: no refill mid-loop
		h := q.middleware(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusOK)
		}))
		serve := func() int {
			rec := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodPost, "/api/links", nil)
			h.ServeHTTP(rec, req.WithContext(authctx.WithPrincipal(req.Context(), editor(7))))
			return rec.Code
		}
		for i := 0; i < want.APIWritesPerMinute; i++ {
			require.Equal(t, http.StatusOK, serve(), "write %d of the compiled budget", i+1)
		}
		assert.Equal(t, http.StatusTooManyRequests, serve())
	})
}

// The expensive set is matched by ROUTE SHAPE, not by substring: "/api/import"
// appearing anywhere in a path must not promote an unrelated route into the
// small bucket, and a parameterised segment must match exactly one segment.
func TestIsExpensive_MatchesTheRouteShapeAndNothingElse(t *testing.T) {
	t.Parallel()
	tests := []struct {
		method, path string
		want         bool
	}{
		{http.MethodPost, "/api/import", true},
		{http.MethodPost, "/api/import/", true},
		{http.MethodPost, "/api/import/apply", true},
		{http.MethodPost, "/api/import/validate", true},
		{http.MethodPost, "/api/backup/restore", true},
		{http.MethodPost, "/api/backup/validate", true},
		{http.MethodPost, "/api/links/42/screenshot", true},
		{http.MethodPost, "/api/links/42/refresh-preview", true},
		// Image ingest decodes up to 50 MP per call (INV-076/077); the body
		// limit bounds the bytes, not the pixels.
		{http.MethodPost, "/api/links/42/image", true},
		{http.MethodPost, "/api/notes/images", true},
		// Chromium fallback + outbound HTTP: the former GET was unmetered.
		{http.MethodPost, "/api/links/url-metadata", true},

		{http.MethodGet, "/api/links/url-metadata", false},
		{http.MethodGet, "/api/import/apply", false},
		{http.MethodGet, "/api/links/42/image", false},
		{http.MethodPost, "/api/links", false},
		{http.MethodPost, "/api/links/42", false},
		{http.MethodPost, "/api/links/42/seen-change", false},
		{http.MethodDelete, "/api/links/42/image", false},
		{http.MethodPost, "/api/links/42/extra/screenshot", false},
		{http.MethodPost, "/api/links//screenshot", false},
		{http.MethodPost, "/api/notimport/apply", false},
		{http.MethodPost, "/api/import/apply/more", false},
	}
	for _, tt := range tests {
		assert.Equalf(t, tt.want, isExpensive(tt.method, tt.path), "%s %s", tt.method, tt.path)
	}
}

// The closed map is only closed around routes that EXIST. A pattern that no
// longer names a mounted route is a hole nobody sees: the route keeps costing
// what it costs and the small bucket silently stops covering it.
func TestExpensiveRoutes_EveryPatternNamesARouteTheRouterMounts(t *testing.T) {
	t.Parallel()
	mounted := map[string]bool{}
	router := New(fullyWiredDeps())
	require.NoError(t, chi.Walk(router.(chi.Routes),
		func(method, route string, _ http.Handler, _ ...func(http.Handler) http.Handler) error {
			mounted[method+" "+normalizeRoutePath(route)] = true
			return nil
		}))

	require.NotEmpty(t, expensiveRoutes, "an empty set would make this guard vacuous")
	for _, pattern := range expensiveRoutes {
		method, path, ok := strings.Cut(pattern, " ")
		require.True(t, ok, "malformed expensive route key %q", pattern)
		assert.Truef(t, mounted[method+" "+normalizeRoutePath(path)],
			"expensive route %q is not mounted by the router — it names nothing", pattern)
	}

	assert.True(t, mounted["POST /api/links/url-metadata"],
		"POST /api/links/url-metadata must be mounted so CSRF, writeGate and the quota apply")
	assert.False(t, mounted["GET /api/links/url-metadata"],
		"GET /api/links/url-metadata must not remain — it bypassed CSRF and the write quota")
}

// When the object store is down, screenshot/upload used to be omitted from the
// mux. The SPA then saw Chi's empty 404 ("Request failed with status code 404")
// on Save after picking a file. The routes must stay mounted so the handler
// can answer 503 with a JSON envelope.
func TestImageRoutesStayMountedWithoutObjectStore(t *testing.T) {
	t.Parallel()
	router := New(Deps{
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
		Config: config.Config{BindAddr: "127.0.0.1"},
	})
	mounted := map[string]bool{}
	require.NoError(t, chi.Walk(router.(chi.Routes),
		func(method, route string, _ http.Handler, _ ...func(http.Handler) http.Handler) error {
			mounted[method+" "+normalizeRoutePath(route)] = true
			return nil
		}))
	for _, pattern := range []string{
		"POST /api/links/{id}/image",
		"DELETE /api/links/{id}/image",
		"POST /api/links/{id}/screenshot",
	} {
		assert.Truef(t, mounted[pattern], "route %q must stay mounted when storage is nil", pattern)
	}
}

func TestStatusRouteIsMounted(t *testing.T) {
	t.Parallel()
	router := New(Deps{
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
		Config: config.Config{BindAddr: "127.0.0.1"},
	})
	mounted := map[string]bool{}
	require.NoError(t, chi.Walk(router.(chi.Routes),
		func(method, route string, _ http.Handler, _ ...func(http.Handler) http.Handler) error {
			mounted[method+" "+normalizeRoutePath(route)] = true
			return nil
		}))
	assert.True(t, mounted["GET /api/status"], "GET /api/status must be mounted even with a zero-value Deps")
}

// A request with no principal reaches this middleware only through a mount
// mistake — the principal middleware above it refuses anonymous callers itself.
// Passing through is deliberate: inventing a shared bucket for "everyone
// unauthenticated" would meter every anonymous visitor as one caller, which is
// a denial of service built out of the defence rather than a quota.
func TestAPIQuota_WithoutAPrincipalTheRequestPassesThrough(t *testing.T) {
	t.Parallel()
	q := newAPIQuota(policyWith(1, 1), nil)
	served := 0
	h := q.middleware(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		served++
		w.WriteHeader(http.StatusOK)
	}))
	for i := 0; i < 5; i++ {
		require.Equal(t, http.StatusOK, hit(h, http.MethodPost, "/api/links").Code)
	}
	assert.Equal(t, 5, served)
	assert.Zero(t, q.writes.Len(), "an unattributable request must not allocate a bucket")
}

// Retry-After is delta-SECONDS and must never be 0: "retry after zero seconds"
// invites the client to retry immediately, which is the loop the 429 exists to
// break. A sub-second wait rounds UP for the same reason.
func TestWriteRateLimited_NeverAdvertisesZeroSeconds(t *testing.T) {
	t.Parallel()
	for _, tt := range []struct {
		in   time.Duration
		want string
	}{
		{0, "1"},
		{time.Millisecond, "1"},
		{999 * time.Millisecond, "1"},
		{time.Second, "1"},
		{1500 * time.Millisecond, "2"},
		{90 * time.Second, "90"},
	} {
		rec := httptest.NewRecorder()
		writeRateLimited(rec, tt.in)
		assert.Equal(t, http.StatusTooManyRequests, rec.Code)
		assert.Equalf(t, tt.want, rec.Header().Get("Retry-After"), "for %s", tt.in)
	}
}

func fullyWiredDeps() Deps {
	return Deps{
		Logger:        slog.New(slog.NewTextHandler(io.Discard, nil)),
		Config:        config.Config{BindAddr: "127.0.0.1"},
		Screenshotter: stubShotter{},
		Storage:       stubUploader{},
		ScreenshotURL: func(context.Context, string) bool { return false },
		StorageBucket: stubBucket{},
	}
}

type stubShotter struct{}

func (stubShotter) Capture(context.Context, string) ([]byte, error) { return nil, nil }

type stubUploader struct{}

func (stubUploader) Upload(context.Context, string, []byte, string) error { return nil }
func (stubUploader) DeleteObject(context.Context, string) error           { return nil }
func (stubUploader) GetObject(context.Context, string) ([]byte, string, error) {
	return nil, "", nil
}

type stubBucket struct{}

func (stubBucket) WalkObjects(context.Context, string, func(backup.ObjectInfo) error) error {
	return nil
}
func (stubBucket) OpenObject(context.Context, string) (io.ReadCloser, error) { return nil, nil }
func (stubBucket) PutObjectStream(context.Context, string, io.Reader, int64, string) error {
	return nil
}
func (stubBucket) ExistingObjects(context.Context, []string) (map[string]bool, error) {
	return nil, nil
}
func (stubBucket) DeleteObjects(context.Context, []string) error { return nil }

var (
	_ links.Screenshotter  = stubShotter{}
	_ links.Uploader       = stubUploader{}
	_ backup.StorageBucket = stubBucket{}
)

// The refusal is recorded ONCE per principal per hour, not once per 429.
//
// A refusal is the one event an attacker produces at will, so a row per
// rejected request would make the trail the amplifier the quota exists to
// remove — the caller would choose how many permanent rows to insert. Recording
// nothing was the other failure: `auth.rate_limited` shipped with a reader and
// no writer, so the anomaly panel's "already throttled" rule could never fire.
func TestAPIQuota_ARefusalIsAuditedOncePerPrincipal(t *testing.T) {
	var recorded int
	q := newAPIQuota(policyWith(1, 1000), func(*http.Request) { recorded++ })
	inner := q.middleware(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	as := func(id int64) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			inner.ServeHTTP(w, r.WithContext(authctx.WithPrincipal(r.Context(), editor(id))))
		})
	}

	refused := 0
	for range 20 {
		if hit(as(7), http.MethodPost, "/api/tags").Code == http.StatusTooManyRequests {
			refused++
		}
	}
	require.Greater(t, refused, 5, "the budget of 1 must produce many refusals for this to measure anything")
	assert.Equal(t, 1, recorded,
		"%d refusals produced %d rows — the caller must not choose how many rows to write", refused, recorded)

	// A different principal is a different budget and a different row.
	hit(as(8), http.MethodPost, "/api/tags")
	require.Equal(t, http.StatusTooManyRequests,
		hit(as(8), http.MethodPost, "/api/tags").Code)
	assert.Equal(t, 2, recorded, "each principal's own lockout is its own signal")
}

// The EXPENSIVE branch has its own refusal path and its own audit call, and
// `policyWith(1, 1000)` never reaches it. Deleting the recorder from that branch
// left the whole suite green.
func TestAPIQuota_AnExpensiveRefusalIsAuditedToo(t *testing.T) {
	t.Parallel()
	var recorded int
	// Writes wide open, expensive at 1: the second import is refused by the
	// hourly bucket and nothing else.
	q := newAPIQuota(policyWith(1000, 1), func(*http.Request) { recorded++ })
	inner := q.middleware(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		inner.ServeHTTP(w, r.WithContext(authctx.WithPrincipal(r.Context(), editor(21))))
	})

	require.Equal(t, http.StatusOK, hit(h, http.MethodPost, "/api/import").Code)
	require.Equal(t, http.StatusTooManyRequests, hit(h, http.MethodPost, "/api/import").Code,
		"the second expensive call must be refused by the hourly bucket")
	assert.Equal(t, 1, recorded, "an expensive-bucket lockout is a lockout and must be recorded")
}

// A nil recorder enforces identically and simply says nothing.
func TestAPIQuota_WithoutARecorderItStillRefuses(t *testing.T) {
	h, _ := quotaHarness(policyWith(1, 1000), editor(9))
	hit(h, http.MethodPost, "/api/tags")
	assert.Equal(t, http.StatusTooManyRequests,
		hit(h, http.MethodPost, "/api/tags").Code)
}
