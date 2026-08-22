package metrics

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/prometheus/client_golang/prometheus"
)

func authedScrape(t *testing.T, m *Metrics, token string) *httptest.ResponseRecorder {
	t.Helper()
	recorder := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	m.Handler(token).ServeHTTP(recorder, req)
	return recorder
}

func TestHandlerRequiresConfiguredBearerToken(t *testing.T) {
	t.Parallel()
	m := New(nil)
	if m.Registerer() == nil {
		t.Fatal("metrics registerer is nil")
	}

	// Empty token = endpoint disabled, not open: shipping open-by-default
	// would expose route shapes and traffic volume on an unauthenticated URL.
	disabled := httptest.NewRecorder()
	m.Handler("").ServeHTTP(disabled, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	if disabled.Code != http.StatusServiceUnavailable {
		t.Fatalf("disabled status = %d", disabled.Code)
	}

	unauthorized := httptest.NewRecorder()
	m.Handler("secret").ServeHTTP(unauthorized, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	if unauthorized.Code != http.StatusUnauthorized || unauthorized.Header().Get("WWW-Authenticate") != "Bearer" {
		t.Fatalf("unauthorized status=%d challenge=%q", unauthorized.Code, unauthorized.Header().Get("WWW-Authenticate"))
	}

	wrongToken := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	req.Header.Set("Authorization", "Bearer not-the-secret")
	m.Handler("secret").ServeHTTP(wrongToken, req)
	if wrongToken.Code != http.StatusUnauthorized {
		t.Fatalf("wrong-token status = %d", wrongToken.Code)
	}

	authorized := authedScrape(t, m, "secret")
	if authorized.Code != http.StatusOK {
		t.Fatalf("authorized status = %d body=%s", authorized.Code, authorized.Body.String())
	}
	if body := authorized.Body.String(); !strings.Contains(body, "go_goroutines") || !strings.Contains(body, "foldex_http_requests_in_flight") {
		t.Fatalf("expected runtime and application metrics, body=%s", body)
	}
}

func TestInstrumentRecordsRouteStatusAndUnmatchedRequests(t *testing.T) {
	t.Parallel()
	m := New(nil)
	router := chi.NewRouter()
	router.Use(m.Instrument)
	router.Get("/api/links/{id}", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusCreated)
	})

	created := httptest.NewRecorder()
	router.ServeHTTP(created, httptest.NewRequest(http.MethodGet, "/api/links/42", nil))
	if created.Code != http.StatusCreated {
		t.Fatalf("created status = %d", created.Code)
	}

	notFound := httptest.NewRecorder()
	router.ServeHTTP(notFound, httptest.NewRequest(http.MethodGet, "/missing", nil))
	if notFound.Code != http.StatusNotFound {
		t.Fatalf("missing status = %d", notFound.Code)
	}

	body := authedScrape(t, m, "token").Body.String()
	// The route label must be chi's PATTERN — the raw path carries ids/slugs
	// (cardinality leak AND data leak on a bookmark manager).
	if strings.Contains(body, "/api/links/42") {
		t.Fatalf("raw URL leaked into labels: %s", body)
	}
	if !strings.Contains(body, `foldex_http_requests_total{method="GET",route="/api/links/{id}",status="201"} 1`) {
		t.Fatalf("route/status metric missing: %s", body)
	}
	if !strings.Contains(body, `foldex_http_requests_total{method="GET",route="unmatched",status="404"} 1`) {
		t.Fatalf("unmatched metric missing: %s", body)
	}
}

func TestInstrumentOutsideChiDoesNotPanic(t *testing.T) {
	t.Parallel()
	m := New(nil)
	h := m.Instrument(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	recorder := httptest.NewRecorder()
	h.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/raw", nil))
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d", recorder.Code)
	}
	body := authedScrape(t, m, "token").Body.String()
	if !strings.Contains(body, `route="unmatched"`) {
		t.Fatalf("request outside chi should count as unmatched: %s", body)
	}
}

func TestInstrumentDoesNotCountMetricsOrProbeRequests(t *testing.T) {
	t.Parallel()
	m := New(nil)
	calls := 0
	h := m.Instrument(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls++
		w.WriteHeader(http.StatusNoContent)
	}))
	for _, path := range []string{"/metrics", "/healthz"} {
		recorder := httptest.NewRecorder()
		h.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, path, nil))
		if recorder.Code != http.StatusNoContent {
			t.Errorf("%s passthrough status=%d", path, recorder.Code)
		}
	}
	if calls != 2 {
		t.Fatalf("passthrough calls=%d", calls)
	}

	families, err := m.registry.Gather()
	if err != nil {
		t.Fatal(err)
	}
	for _, family := range families {
		if family.GetName() == "foldex_http_requests_total" && len(family.Metric) != 0 {
			t.Fatalf("operational endpoint was instrumented: %+v", family.Metric)
		}
	}
}

// r.Method is client-controlled and client_golang never prunes series: every
// distinct method would become a permanent label value on an endpoint mounted
// before auth. Everything outside the standard set collapses into "other".
func TestMetricMethodCollapsesUnknownMethods(t *testing.T) {
	t.Parallel()
	for _, method := range []string{
		http.MethodGet, http.MethodPost, http.MethodPut, http.MethodPatch,
		http.MethodDelete, http.MethodHead, http.MethodOptions,
		http.MethodConnect, http.MethodTrace,
	} {
		if got := metricMethod(method); got != method {
			t.Errorf("metricMethod(%q) = %q", method, got)
		}
	}
	for _, method := range []string{"PROPFIND", "YOLO", "get", ""} {
		if got := metricMethod(method); got != "other" {
			t.Errorf("metricMethod(%q) = %q, want other", method, got)
		}
	}
}

func TestInstrumentCollapsesUnknownMethodLabel(t *testing.T) {
	t.Parallel()
	m := New(nil)
	// Plain handler, not a chi route: chi refuses to REGISTER non-standard
	// methods, but nothing stops a client from SENDING one — and the router's
	// 405/404 answer still flows through Instrument, which is exactly the
	// unauthenticated path that would mint unbounded label values.
	h := m.Instrument(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	recorder := httptest.NewRecorder()
	h.ServeHTTP(recorder, httptest.NewRequest("PROPFIND", "/dav", nil))
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d", recorder.Code)
	}
	body := authedScrape(t, m, "token").Body.String()
	if strings.Contains(body, `method="PROPFIND"`) {
		t.Fatalf("unbounded method label leaked into the registry: %s", body)
	}
	if !strings.Contains(body, `method="other"`) {
		t.Fatalf("non-standard method was not collapsed into other: %s", body)
	}
}

type blockingCollector struct {
	desc    *prometheus.Desc
	started chan struct{}
	release chan struct{}
	once    sync.Once
}

func (c *blockingCollector) Describe(ch chan<- *prometheus.Desc) { ch <- c.desc }

func (c *blockingCollector) Collect(ch chan<- prometheus.Metric) {
	c.once.Do(func() { close(c.started) })
	<-c.release
	ch <- prometheus.MustNewConstMetric(c.desc, prometheus.GaugeValue, 1)
}

// A scrape walks every collector; MaxRequestsInFlight=1 keeps a client that
// stacks requests from stacking Gather calls.
func TestHandlerLimitsConcurrentScrapes(t *testing.T) {
	t.Parallel()
	m := New(nil)
	collector := &blockingCollector{
		desc:    prometheus.NewDesc("test_blocking_metric", "test", nil, nil),
		started: make(chan struct{}),
		release: make(chan struct{}),
	}
	m.registry.MustRegister(collector)
	handler := m.Handler("token")

	firstDone := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		recorder := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
		req.Header.Set("Authorization", "Bearer token")
		handler.ServeHTTP(recorder, req)
		firstDone <- recorder
	}()
	<-collector.started

	second := httptest.NewRecorder()
	secondReq := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	secondReq.Header.Set("Authorization", "Bearer token")
	handler.ServeHTTP(second, secondReq)
	if second.Code != http.StatusServiceUnavailable {
		t.Fatalf("concurrent scrape status = %d, want 503", second.Code)
	}

	close(collector.release)
	if first := <-firstDone; first.Code != http.StatusOK {
		t.Fatalf("first scrape status = %d", first.Code)
	}
}

// backup's extendArchiveDeadlines stretches multi-GB stream deadlines through
// http.NewResponseController, which traverses writer wrappers via Unwrap().
// The instrumentation wrapper must stay traversable or those calls silently
// fail and streams get cut at the server's default WriteTimeout. Needs a real
// http.Server: httptest.ResponseRecorder has no deadline support to unwrap to.
func TestInstrumentPreservesResponseControllerDeadlines(t *testing.T) {
	t.Parallel()
	m := New(nil)
	deadlineErr := make(chan error, 1)
	srv := httptest.NewServer(m.Instrument(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		deadlineErr <- http.NewResponseController(w).SetWriteDeadline(time.Now().Add(time.Minute))
	})))
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/stream")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if err := <-deadlineErr; err != nil {
		t.Fatalf("SetWriteDeadline through Instrument = %v — the wrapper broke the Unwrap chain", err)
	}
}

// pgxpool.New is lazy — it never dials until a query runs — so an unreachable
// DSN is enough to exercise the collector against a real pool.
func TestPoolCollectorExportsPoolStatistics(t *testing.T) {
	t.Parallel()
	pool, err := pgxpool.New(context.Background(), "postgres://test:test@127.0.0.1:1/test?sslmode=disable&pool_max_conns=3")
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	m := New(pool)

	scrape := authedScrape(t, m, "token")
	if scrape.Code != http.StatusOK {
		t.Fatalf("scrape status = %d", scrape.Code)
	}
	body := scrape.Body.String()
	for _, name := range []string{
		"foldex_db_pool_total_conns",
		"foldex_db_pool_acquired_conns",
		"foldex_db_pool_idle_conns",
		"foldex_db_pool_max_conns 3",
		"foldex_db_pool_acquire_count",
		"foldex_db_pool_empty_acquire_count",
		"foldex_db_pool_empty_acquire_wait_seconds",
		"foldex_db_pool_canceled_acquire_count",
		"foldex_db_pool_max_lifetime_destroy_count",
	} {
		if !strings.Contains(body, name) {
			t.Errorf("pool metric %q missing: %s", name, body)
		}
	}
}
