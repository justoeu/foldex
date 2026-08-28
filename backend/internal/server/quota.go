package server

import (
	"context"
	"net/http"
	"strconv"
	"strings"
	"time"

	"foldex/internal/abusepolicy"
	"foldex/internal/pkg/authctx"
	"foldex/internal/pkg/httperr"
	"foldex/internal/pkg/quota"
)

// policyReader is the live-policy seam. *abusepolicy.Cache satisfies it, and a
// nil one answers the compiled defaults rather than nothing — an unwired
// dependency must not switch a defence off silently.
type policyReader interface {
	Current(context.Context) abusepolicy.Policy
}

// expensiveRoutes is the closed set of routes that cost far more than one row.
//
// Keyed by METHOD + chi ROUTE PATTERN, the same vocabulary contentAudit uses
// and for the same reason: "/api/links/{id}/screenshot" is one entry however
// many links exist, and a raw-path map would either miss every row or grow a
// matcher of its own. Unlike contentAudit this one is matched BEFORE the
// handler runs — a quota that decided after the work was done would be a
// report, not a control — so the pattern is matched against the request path by
// matchesPattern rather than read back out of chi's route context, which chi
// only fills during routing.
//
// A closed map, and absence means "ordinary write". That is deliberate: a new
// route joining the small hourly bucket should be a decision someone made, not
// a consequence of its URL shape. What keeps the map from rotting is
// TestExpensiveRoutes_EveryPatternNamesARouteTheRouterMounts — a pattern that
// stops naming a mounted route fails the build instead of quietly covering
// nothing.
//
// Membership is "does one request do external I/O, spawn a browser, or stream
// the whole tenant?", which is why backup EXPORT is here alongside restore even
// though SDD §5.3 names only restore: export walks every row and every object
// the caller owns into one stream, and is the single most expensive thing this
// instance can be asked to do.
var expensiveRoutes = map[string]struct{}{
	"POST /api/import":                     {}, // parses an uploaded bookmarks file
	"POST /api/import/validate":            {},
	"POST /api/import/apply":               {}, // inserts rows and enqueues previews
	"POST /api/backup":                     {}, // streams the whole library out
	"POST /api/backup/download":            {},
	"POST /api/backup/restore":             {},
	"POST /api/links/{id}/screenshot":      {}, // launches Chromium
	"POST /api/links/{id}/refresh-preview": {}, // outbound fetch per call
}

// quotaMaxPrincipals bounds each limiter's bucket map. See quota's own note on
// the ceiling: the key space is already the accounts on the instance, and this
// is what keeps that from being an assumption instead of a bound.
const quotaMaxPrincipals = 10_000

// apiQuota rations mutating requests per authenticated principal — SDD §5.3.
//
// Per PRINCIPAL, not per route, and that is the whole design. A per-route quota
// lets one caller hold the sixteen-connection pool by spreading a loop across
// twenty endpoints, each of them individually well behaved; the pool does not
// care which URL exhausted it.
//
// No role is exempt, the owner included. An exemption would be an account able
// to take the instance down, and the first person to trip over it would be the
// operator running a large import.
type apiQuota struct {
	pol       policyReader
	writes    *quota.Limiter
	expensive *quota.Limiter
}

func newAPIQuota(pol policyReader) *apiQuota {
	return &apiQuota{
		pol:       pol,
		writes:    quota.New(time.Minute, quotaMaxPrincipals),
		expensive: quota.New(time.Hour, quotaMaxPrincipals),
	}
}

func (q *apiQuota) current(ctx context.Context) abusepolicy.Policy {
	if q.pol == nil {
		return abusepolicy.Default()
	}
	return q.pol.Current(ctx)
}

func (q *apiQuota) writeLimit(ctx context.Context) int {
	return q.current(ctx).APIWritesPerMinute
}

func (q *apiQuota) expensiveLimit(ctx context.Context) int {
	return q.current(ctx).APIExpensivePerHour
}

// middleware charges the request and answers 429 when the budget is spent.
//
// Mounted inside the principal group, so the identity it keys on is already
// resolved. Reads pass untouched: a read is one indexed SELECT, and metering it
// would spend a person's budget on browsing their own library while doing
// nothing about the write amplification the quota exists for.
func (q *apiQuota) middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !mutating(r.Method) {
			next.ServeHTTP(w, r)
			return
		}
		p, ok := authctx.FromContext(r.Context())
		if !ok {
			// Unreachable through the router: this middleware sits below the
			// principal middleware, which refuses anonymous callers itself.
			// Passing through rather than inventing a shared bucket keeps a
			// future mount site from silently metering every anonymous visitor
			// as one principal — which is a denial of service, not a quota.
			next.ServeHTTP(w, r)
			return
		}

		// Keyed by ACCOUNT, never by session or token id. A bearer token is a
		// credential of an account, not a second account, so minting one must
		// not multiply the budget (INV-023 draws the same line for scope).
		key := strconv.FormatInt(int64(p.UserID), 10)
		ctx := r.Context()

		expensiveLimit := q.expensiveLimit(ctx)
		charged := false
		if isExpensive(r.Method, r.URL.Path) {
			if d := q.expensive.Allow(key, expensiveLimit); !d.Allowed {
				writeRateLimited(w, d.RetryAfter)
				return
			}
			charged = true
		}

		writeLimit := q.writeLimit(ctx)
		if d := q.writes.Allow(key, writeLimit); !d.Allowed {
			if charged {
				// The expensive token bought nothing: the request is being
				// refused and no work will run. Keeping it would mean that
				// hitting the per-minute ceiling also drains the hourly one,
				// so a burst of ordinary writes would quietly cost a user
				// their imports for the rest of the hour.
				q.expensive.Refund(key, expensiveLimit)
			}
			writeRateLimited(w, d.RetryAfter)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// writeRateLimited answers 429 with a Retry-After the client can act on.
//
// 429 and not a dropped connection: the caller is legitimate until proven
// otherwise — the overwhelmingly likely one is a script of their own with a
// loop in it — and a client that is told how long to wait can back off. A
// connection that simply dies teaches a retry loop to hammer harder.
func writeRateLimited(w http.ResponseWriter, retryAfter time.Duration) {
	seconds := int(retryAfter / time.Second)
	if retryAfter%time.Second != 0 {
		seconds++
	}
	if seconds < 1 {
		seconds = 1
	}
	w.Header().Set("Retry-After", strconv.Itoa(seconds))
	httperr.Write(w, httperr.New(http.StatusTooManyRequests, "rate_limited",
		"too many requests — this account's write quota for this instance is spent; retry in "+
			strconv.Itoa(seconds)+"s"))
}

// isExpensive reports whether this request belongs in the small hourly bucket.
func isExpensive(method, path string) bool {
	path = normalizeRoutePath(path)
	for pattern := range expensiveRoutes {
		m, p, ok := strings.Cut(pattern, " ")
		if ok && m == method && matchesPattern(normalizeRoutePath(p), path) {
			return true
		}
	}
	return false
}

// matchesPattern compares a chi pattern to a path, segment by segment.
//
// Segment-wise rather than by prefix or substring, because both of those are
// wrong in a way that only shows up later: a prefix test would put
// "/api/import-history" in the expensive bucket, and a substring test would put
// anything containing the word anywhere. A "{param}" segment matches exactly
// one NON-EMPTY segment, so "/api/links//screenshot" — which routes nowhere —
// does not match either.
func matchesPattern(pattern, path string) bool {
	pp := strings.Split(pattern, "/")
	sp := strings.Split(path, "/")
	if len(pp) != len(sp) {
		return false
	}
	for i := range pp {
		if strings.HasPrefix(pp[i], "{") && strings.HasSuffix(pp[i], "}") {
			if sp[i] == "" {
				return false
			}
			continue
		}
		if pp[i] != sp[i] {
			return false
		}
	}
	return true
}

// normalizeRoutePath drops a trailing slash so "/api/import" and the
// "/api/import/" chi reports for a Route("/import") + Post("/") pair are the
// same route. Without it the guard test would call a mounted route missing.
func normalizeRoutePath(p string) string {
	if len(p) > 1 && strings.HasSuffix(p, "/") {
		return strings.TrimSuffix(p, "/")
	}
	return p
}
