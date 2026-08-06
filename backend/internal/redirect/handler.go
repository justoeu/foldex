package redirect

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"

	"foldex/internal/pkg/httperr"
)

// LinkResolver resolves /go targets (satisfied by *links.Repository).
type LinkResolver interface {
	ClickAndResolve(ctx context.Context, id int64) (string, error)
	ClickAndResolveBySlug(ctx context.Context, slug string) (string, error)
}

type Handler struct {
	repo LinkResolver
	// allowNumericIDs re-enables the legacy /go/42 form. Off by default since
	// ADR-32 — see redirect() for why.
	allowNumericIDs bool
}

// NewHandler builds the /go handler. allowNumericIDs is the GO_NUMERIC_IDS
// escape hatch for an instance that has old /go/42 links in the wild.
func NewHandler(repo LinkResolver, allowNumericIDs bool) *Handler {
	return &Handler{repo: repo, allowNumericIDs: allowNumericIDs}
}

func (h *Handler) Mount(r chi.Router) {
	// Param name stays "id" so the chi route doesn't change shape — what
	// flows through it can be either a numeric ID or a slug, decided in
	// h.redirect at request time.
	r.Get("/go/{id}", h.redirect)
}

// redirect resolves /go/{value} where {value} is a slug — or, when the
// operator has opted in, a numeric link id.
//
// **Numeric ids are OFF by default (ADR-32).** This route resolves with NO
// session: it is a public share link, so there is no tenant to scope the lookup
// by. `link.id` is a dense global BIGSERIAL now shared across every account, so
// leaving /go/42 enabled would let anyone walk 1, 2, 3… and enumerate — and
// silently CLICK-LOG — every link on the instance, including other people's.
// Slugs are not a secret either, but they are not a counter: you cannot
// discover the next one by adding 1.
//
// The escape hatch exists because old /go/42 links are already shared
// somewhere, and an upgrade that breaks every one of them without a way back
// is not an upgrade. The operator who turns it on is accepting the enumeration
// in exchange, which is a choice only they can make.
//
// The DB CHECK constraint on link.slug forbids pure-numeric values, so the
// branches can't collide: a value that parses as int can't ever be a slug.
func (h *Handler) redirect(w http.ResponseWriter, r *http.Request) {
	raw := chi.URLParam(r, "id")
	if raw == "" {
		httperr.Write(w, httperr.New(http.StatusBadRequest, "invalid_target", "target is required"))
		return
	}

	if id, err := strconv.ParseInt(raw, 10, 64); err == nil && id > 0 {
		if !h.allowNumericIDs {
			// A plain 404, the same answer an unknown slug gets. Anything more
			// specific ("numeric lookup is disabled") would confirm the id
			// space is real and worth probing once the flag flips.
			httperr.Write(w, httperr.ErrNotFound)
			return
		}
		dest, err := h.repo.ClickAndResolve(r.Context(), id)
		if err == nil {
			redirect(w, r, dest)
			return
		}
		// A pure-numeric value can never be a slug (CHECK constraint), so
		// not-found here is terminal.
		httperr.Write(w, err)
		return
	}

	dest, err := h.repo.ClickAndResolveBySlug(r.Context(), raw)
	if err != nil {
		if errors.Is(err, httperr.ErrNotFound) {
			httperr.Write(w, httperr.ErrNotFound)
			return
		}
		httperr.Write(w, err)
		return
	}
	redirect(w, r, dest)
}

// redirect validates the destination scheme before issuing the redirect.
// All write paths enforce http(s)://, but defense-in-depth here catches any
// future regression or direct-DB manipulation that could plant a
// javascript:/data:/file: URL in the link table.
func redirect(w http.ResponseWriter, r *http.Request, dest string) {
	if !strings.HasPrefix(dest, "http://") && !strings.HasPrefix(dest, "https://") {
		httperr.Write(w, httperr.New(http.StatusBadRequest, "invalid_target", "unsupported scheme"))
		return
	}
	http.Redirect(w, r, dest, http.StatusFound)
}
