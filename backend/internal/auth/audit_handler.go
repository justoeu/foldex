package auth

import (
	"encoding/csv"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"

	"foldex/internal/pkg/authctx"
	"foldex/internal/pkg/httperr"
)

// auditWindows are the periods the screen offers. A closed set rather than a
// free duration: an arbitrary "since" is an arbitrary amount of scanning a
// caller can ask the database for, and three buttons is what the screen has.
var auditWindows = map[string]time.Duration{
	"24h": 24 * time.Hour,
	"7d":  7 * 24 * time.Hour,
	"30d": 30 * 24 * time.Hour,
}

// auditWindowDefault is what a request naming no range gets. The screen opens
// on it, so it is the period most requests actually run under.
const auditWindowDefault = "7d"

// parseAuditFilter reads the query string into a filter, refusing anything the
// vocabulary does not contain.
//
// An unknown action is a 400 rather than an empty page: without the check it
// would run a full backward scan of the window to return nothing, which is the
// cheapest way for a caller to make the database work on their behalf.
func parseAuditFilter(r *http.Request) (AuditFilter, error) {
	q := r.URL.Query()
	f := AuditFilter{Action: q.Get("action"), Search: q.Get("q")}
	if f.Action != "" && !KnownAuditAction(f.Action) {
		return f, httperr.New(http.StatusBadRequest, "invalid_action", "unknown audit action")
	}
	switch c := q.Get("category"); c {
	case "", CategoryContent, CategoryIdentity:
		f.Category = c
	default:
		return f, httperr.New(http.StatusBadRequest, "invalid_category",
			"category must be identity or content")
	}
	window := q.Get("window")
	if window == "" {
		window = auditWindowDefault
	}
	d, ok := auditWindows[window]
	if !ok {
		return f, httperr.New(http.StatusBadRequest, "invalid_window", "window must be 24h, 7d or 30d")
	}
	f.Since = time.Now().Add(-d)

	before, err := optionalInt64(q.Get("before"))
	if err != nil || before < 0 {
		return f, httperr.New(http.StatusBadRequest, "invalid_cursor", "before must be an id")
	}
	f.BeforeID = before
	// "asc" rather than a boolean-ish "sort=oldest": one spelling, and anything
	// else is the default rather than an error — a sort order is a preference,
	// not a filter that can quietly change which rows are returned.
	f.Ascending = q.Get("order") == "asc"
	limit, err := optionalInt64(q.Get("limit"))
	// Range-checked BEFORE the conversion, not after. ListAudit clamps its own
	// argument, but by then the int64 has already been narrowed to int — and on
	// a 32-bit build a value like 2^32+50 truncates to 50, arriving as a
	// perfectly plausible number that no clamp can recognise as garbage.
	if err != nil || limit < 0 || limit > maxAuditPageSize {
		return f, httperr.New(http.StatusBadRequest, "invalid_limit",
			fmt.Sprintf("limit must be a number between 0 and %d", maxAuditPageSize))
	}
	f.Limit = int(limit)
	return f, nil
}

// ListAudit serves one page of the administrative trail.
func (h *AdminHandler) ListAudit(w http.ResponseWriter, r *http.Request) {
	f, err := parseAuditFilter(r)
	if err != nil {
		httperr.Write(w, err)
		return
	}
	entries, err := h.repo.ListAudit(r.Context(), f)
	if err != nil {
		h.logger.Error("admin list audit", "err", err)
		httperr.Write(w, httperr.ErrInternal)
		return
	}
	h.raiseBurstSeverity(r, entries, f.Since)
	httperr.JSON(w, http.StatusOK, map[string]any{"entries": entries})
}

// raiseBurstSeverity promotes login.failed rows whose address is inside a
// burst, so one wrong password and a run of five read differently on a screen
// somebody scans rather than reads.
//
// A failure to compute the counts LEAVES the floor severity in place rather
// than failing the page: the entries are already correct, and answering 500
// because a secondary aggregate was unavailable would hide the trail during
// exactly the incident it exists for.
func (h *AdminHandler) raiseBurstSeverity(r *http.Request, entries []AuditEntry, since time.Time) {
	if !containsFailure(entries) {
		return
	}
	bursts, err := h.repo.FailureBursts(r.Context(), since)
	if err != nil {
		h.logger.Warn("audit failure bursts", "err", err)
		return
	}
	applyBursts(entries, bursts)
}

func containsFailure(entries []AuditEntry) bool {
	for _, e := range entries {
		if e.Action == AuditLoginFailed {
			return true
		}
	}
	return false
}

// applyBursts is the half of raiseBurstSeverity that does no I/O, so a caller
// paging through many pages can compute the counts ONCE and reuse them. The
// counts describe the whole window, and the window does not move between
// pages — running the aggregate per page is the same answer, re-derived by a
// full GROUP BY over the window each time.
func applyBursts(entries []AuditEntry, bursts map[string]int) {
	for i := range entries {
		if entries[i].Action != AuditLoginFailed || entries[i].IP == nil {
			continue
		}
		entries[i].Severity = AuditSeverity(AuditLoginFailed, bursts[*entries[i].IP])
	}
}

// AuditStats serves the screen's header: totals, the daily chart, the
// distribution, the busiest actors and origins, and the worst burst.
func (h *AdminHandler) AuditStats(w http.ResponseWriter, r *http.Request) {
	f, err := parseAuditFilter(r)
	if err != nil {
		httperr.Write(w, err)
		return
	}
	stats, err := h.repo.AuditStatsSince(r.Context(), f.Since)
	if err != nil {
		h.logger.Error("admin audit stats", "err", err)
		httperr.Write(w, httperr.ErrInternal)
		return
	}
	httperr.JSON(w, http.StatusOK, stats)
}

// maxAuditExportRows bounds one CSV.
//
// The export streams, so memory is not the constraint; the database's time is.
// Ten thousand rows is a spreadsheet a person can actually open, and an
// investigation needing more has the window filters to narrow with.
const maxAuditExportRows = 10000

// ExportAudit streams the current filter as CSV.
//
// It walks the keyset in pages rather than issuing one unbounded query: the
// same cursor the screen pages with, applied until the cap. That keeps one
// export from holding a single statement open across the whole window while
// the instance serves other traffic.
//
// The subject column is absent here for the same reason it is absent from
// ListAudit — this is the administrative projection, and a CSV is the easiest
// possible way for content to leave the instance.
func (h *AdminHandler) ExportAudit(w http.ResponseWriter, r *http.Request) {
	f, err := parseAuditFilter(r)
	if err != nil {
		httperr.Write(w, err)
		return
	}
	f.Limit = maxAuditPageSize

	// Headers before the first row: once anything is written the status is
	// committed, and an error past that point cannot become a 500 — it can only
	// truncate the file. Everything that can fail early is done above.
	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Content-Disposition",
		fmt.Sprintf(`attachment; filename="foldex-audit-%s.csv"`, time.Now().UTC().Format("20060102-150405")))
	// The trail is per-instance state that changes constantly; a cached copy
	// served to a later investigation would be worse than no export.
	w.Header().Set("Cache-Control", "no-store")

	cw := csv.NewWriter(w)
	defer cw.Flush()
	_ = cw.Write([]string{"id", "created_at", "action", "category", "severity",
		"actor", "actor_ref", "target", "detail", "ip", "ip_trusted", "user_agent"})

	// Computed ONCE, not per page. The counts are an aggregate over the whole
	// window and the window does not move as the cursor advances, so calling
	// this inside the loop would re-run a GROUP BY over every failed login in
	// the period for each of up to fifty pages — one export turning into fifty
	// identical scans of the table's bulk. A failure leaves the floor severity
	// in place rather than aborting a download that is otherwise correct.
	bursts, err := h.repo.FailureBursts(r.Context(), f.Since)
	if err != nil {
		h.logger.Warn("audit export failure bursts", "err", err)
		bursts = map[string]int{}
	}

	written := 0
	for written < maxAuditExportRows {
		entries, err := h.repo.ListAudit(r.Context(), f)
		if err != nil {
			h.logger.Error("admin export audit", "err", err)
			return
		}
		if len(entries) == 0 {
			return
		}
		applyBursts(entries, bursts)
		for _, e := range entries {
			if err := cw.Write(auditCSVRow(e)); err != nil {
				// The client hung up mid-download. Ordinary, not an error worth
				// an entry of its own.
				return
			}
			written++
			f.BeforeID = e.ID
			if written >= maxAuditExportRows {
				return
			}
		}
		cw.Flush()
		if err := cw.Error(); err != nil {
			return
		}
	}
}

func auditCSVRow(e AuditEntry) []string {
	str := func(p *string) string {
		if p == nil {
			return ""
		}
		return *p
	}
	ref := ""
	if e.ActorRef != nil {
		ref = strconv.FormatInt(*e.ActorRef, 10)
	}
	return []string{
		strconv.FormatInt(e.ID, 10),
		e.CreatedAt.UTC().Format(time.RFC3339),
		e.Action, e.Category, e.Severity,
		csvSafe(str(e.ActorEmail)), ref, csvSafe(str(e.TargetEmail)), csvSafe(str(e.Detail)),
		str(e.IP), strconv.FormatBool(e.IPTrusted), csvSafe(str(e.UserAgent)),
	}
}

// csvSafe defuses spreadsheet formula injection.
//
// A cell beginning with =, +, - or @ is executed as a formula by Excel, Sheets
// and LibreOffice on open. The trail records values an UNAUTHENTICATED caller
// controls — the attempted address on a failed login, and the User-Agent header
// on every row — so "=HYPERLINK(...)" is a payload anyone can write into a file
// an administrator will later open on their own machine. Prefixing an
// apostrophe is what those applications treat as "this is text".
func csvSafe(v string) string {
	if v == "" {
		return v
	}
	switch v[0] {
	case '=', '+', '-', '@', '\t', '\r':
		return "'" + v
	}
	return v
}

// ListIPBlocks serves the permanent blocklist.
func (h *AdminHandler) ListIPBlocks(w http.ResponseWriter, r *http.Request) {
	blocks, err := h.repo.ListIPBlocks(r.Context())
	if err != nil {
		h.logger.Error("admin list ip blocks", "err", err)
		httperr.Write(w, httperr.ErrInternal)
		return
	}
	httperr.JSON(w, http.StatusOK, map[string]any{"blocks": blocks, "max": MaxIPBlocks})
}

type blockIPInput struct {
	IP     string `json:"ip"`
	Reason string `json:"reason"`
}

// BlockIP installs a permanent block.
//
// Every rail is applied here, in front of the write, and each returns its own
// code so the screen can say WHICH one fired. "Invalid address" in front of a
// control that can make the instance unreachable is not something an operator
// can act on.
func (h *AdminHandler) BlockIP(w http.ResponseWriter, r *http.Request) {
	in, derr := httperr.DecodeJSON[blockIPInput](w, r)
	if derr != nil {
		httperr.Write(w, derr)
		return
	}
	ip, err := ValidateBlockIP(in.IP, h.callerIP(r), h.isTrustedProxy)
	if err != nil {
		httperr.Write(w, blockError(err))
		return
	}
	var actor *authctx.UserID
	actorEmail := ""
	if caller, ok := authctx.FromContext(r.Context()); ok && caller.UserID != 0 {
		id := caller.UserID
		actor = &id
		if u, err := h.repo.GetUser(r.Context(), id); err == nil {
			actorEmail = u.Email
		}
	}
	block, err := h.repo.BlockIP(r.Context(), ip, in.Reason, actor, actorEmail)
	if err != nil {
		if err == ErrBlockFull {
			httperr.Write(w, blockError(err))
			return
		}
		h.logger.Error("admin block ip", "err", err)
		httperr.Write(w, httperr.ErrInternal)
		return
	}
	// Enforced from the next request rather than up to a TTL later — the
	// person who clicked is watching.
	if h.blocklist != nil {
		h.blocklist.Invalidate()
	}
	h.audit(r, AuditIPBlocked, nil, ip)
	httperr.JSON(w, http.StatusCreated, block)
}

// UnblockIP removes a block. Idempotent: 204 whether or not a row was there,
// because "this address is not blocked" is the state the caller asked for.
func (h *AdminHandler) UnblockIP(w http.ResponseWriter, r *http.Request) {
	ip := NormalizeIP(chi.URLParam(r, "ip"))
	if ip == "" {
		httperr.Write(w, httperr.New(http.StatusBadRequest, "invalid_ip", "not an ip address"))
		return
	}
	if _, err := h.repo.UnblockIP(r.Context(), ip); err != nil {
		h.logger.Error("admin unblock ip", "err", err)
		httperr.Write(w, httperr.ErrInternal)
		return
	}
	if h.blocklist != nil {
		h.blocklist.Invalidate()
	}
	h.audit(r, AuditIPUnblocked, nil, ip)
	w.WriteHeader(http.StatusNoContent)
}

// blockError maps a rail to its transport shape. The message is the rail's own
// sentence: these describe the OPERATOR's situation, not the instance's
// secrets, and a caller told "that is the address you are connected from" can
// fix it, where one told "invalid" cannot.
func blockError(err error) error {
	switch err {
	case ErrBlockSelf:
		return httperr.New(http.StatusConflict, "block_self", err.Error())
	case ErrBlockLoopback:
		return httperr.New(http.StatusConflict, "block_loopback", err.Error())
	case ErrBlockProxy:
		return httperr.New(http.StatusConflict, "block_proxy", err.Error())
	case ErrBlockFull:
		return httperr.New(http.StatusConflict, "block_full", err.Error())
	default:
		return httperr.New(http.StatusBadRequest, "invalid_ip", ErrBlockMalformed.Error())
	}
}

// callerIP is the address THIS request arrived from, for the self-block rail.
func (h *AdminHandler) callerIP(r *http.Request) string { return NormalizeIP(r.RemoteAddr) }

// ListOwnActivity serves the signed-in account its own content activity.
//
// Not under /api/admin: this is the caller reading their own rows, which needs
// no administrative permission and must work for a viewer. The scope is the
// principal — INV-001's explicit uid — and the subject column is projected
// here and nowhere else.
func (h *Handler) ListOwnActivity(w http.ResponseWriter, r *http.Request) {
	caller, ok := authctx.FromContext(r.Context())
	if !ok || caller.UserID == 0 {
		httperr.Write(w, httperr.New(http.StatusUnauthorized, "unauthenticated", "sign in first"))
		return
	}
	before, err := optionalInt64(r.URL.Query().Get("before"))
	if err != nil || before < 0 {
		httperr.Write(w, httperr.New(http.StatusBadRequest, "invalid_cursor", "before must be an id"))
		return
	}
	limit, err := optionalInt64(r.URL.Query().Get("limit"))
	if err != nil || limit < 0 || limit > maxAuditPageSize {
		httperr.Write(w, httperr.New(http.StatusBadRequest, "invalid_limit",
			fmt.Sprintf("limit must be a number between 0 and %d", maxAuditPageSize)))
		return
	}
	entries, err := h.repo.ListOwnActivity(r.Context(), int64(caller.UserID), before, int(limit))
	if err != nil {
		h.logger.Error("own activity", "err", err)
		httperr.Write(w, httperr.ErrInternal)
		return
	}
	httperr.JSON(w, http.StatusOK, map[string]any{"entries": entries})
}

// auditActionsPayload is the vocabulary, so the screen renders exactly the
// filters this binary can produce instead of a list copied into the client that
// drifts the first time one is added.
func auditActionsPayload() []map[string]string {
	out := make([]map[string]string, 0, len(auditActionOrder))
	for _, a := range auditActionOrder {
		out = append(out, map[string]string{
			"action":   a,
			"category": AuditCategory(a),
			"severity": AuditSeverity(a, 0),
		})
	}
	return out
}

// AuditVocabulary serves the action list the filters are built from.
func (h *AdminHandler) AuditVocabulary(w http.ResponseWriter, r *http.Request) {
	httperr.JSON(w, http.StatusOK, map[string]any{
		"actions": auditActionsPayload(),
		"windows": []string{"24h", "7d", "30d"},
	})
}
