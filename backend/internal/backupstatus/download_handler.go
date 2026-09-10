package backupstatus

import (
	"context"
	"errors"
	"io"
	"net/http"
	"path"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"

	"foldex/internal/pkg/authctx"
	"foldex/internal/pkg/httperr"
)

// minDownloadPassphrase mirrors the agent's floor (backupagent.minArtifactPassphrase).
//
// Restated rather than imported: the agent is a separate binary that may be an
// older build, so it enforces its own floor and this one refuses early with a
// message a human can act on. Two copies of a NUMBER whose disagreement is
// harmless — the stricter of the two wins and both are floors — is a different
// thing from two copies of a security CHECK.
const minDownloadPassphrase = 12

type downloadRequest struct {
	Password string `json:"password"`
}

/*
Download streams one backup artifact to the administrator who asked for it.

Three things happen in order, and the order is the contract:

 1. The budget is SPENT (ReserveDownload) before a byte moves. Charging on
    completion would let a caller take 99% and abort in a loop.
 2. The agent produces the bytes, already encrypted under the caller's
    passphrase. This process never holds the artifact in the clear.
 3. The trail records it. A dump leaving the instance is not a routine event.

The passphrase is read from the BODY and never logged, never persisted and
never echoed — there is deliberately no column for it anywhere.
*/
func (h *Handler) Download(w http.ResponseWriter, r *http.Request) {
	if !h.artifacts.Enabled() {
		// 404, not 503: with the bridge off this route is not a feature that is
		// temporarily down, it is a feature the instance does not have — and
		// the admin surface's own convention is to not confirm what it does not
		// serve (INV-043's reasoning, one level in).
		httperr.Write(w, httperr.ErrNotFound)
		return
	}
	runID, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil || runID <= 0 {
		httperr.Write(w, httperr.New(http.StatusBadRequest, "invalid_run", "run id must be a positive integer"))
		return
	}

	// The shared decoder, not a hand-rolled one: it already caps the body
	// (INV-089) and answers the house's error envelope.
	in, decodeErr := httperr.DecodeJSON[downloadRequest](w, r)
	if decodeErr != nil {
		httperr.Write(w, decodeErr)
		return
	}
	if len([]rune(in.Password)) < minDownloadPassphrase {
		httperr.Write(w, httperr.New(http.StatusBadRequest, "password_too_short",
			"the download password must be at least "+strconv.Itoa(minDownloadPassphrase)+" characters"))
		return
	}

	key, err := h.repo.ArtifactKeyForRun(r.Context(), runID)
	if errors.Is(err, ErrRunNotFound) {
		httperr.Write(w, httperr.ErrNotFound)
		return
	}
	if err != nil {
		h.logger.Error("artifact key lookup", "err", err)
		httperr.Write(w, httperr.ErrInternal)
		return
	}
	if key == "" {
		// A mirror or user_zip run ships N objects and names none of them on
		// its row. Saying so beats a 404 that would read as "this run does not
		// exist" for a run the operator is looking straight at.
		httperr.Write(w, httperr.New(http.StatusConflict, "no_artifact",
			"this run shipped no single artifact — list its objects instead"))
		return
	}

	principal, _ := authctx.FromContext(r.Context())
	if !mayDownload(principal, key) {
		// 404, not 403: the same reasoning the admin surface uses throughout —
		// a refusal that names what exists is a refusal that maps it.
		httperr.Write(w, httperr.ErrNotFound)
		return
	}

	uid := authctx.MustUser(r.Context())
	reservation, err := h.repo.ReserveDownload(r.Context(), runID, uid, key)
	if errors.Is(err, ErrDownloadBudgetSpent) {
		httperr.Write(w, httperr.New(http.StatusTooManyRequests, "download_budget_spent",
			"this artifact has already been downloaded "+strconv.Itoa(MaxDownloadsPerAdmin)+" times by this account"))
		return
	}
	if err != nil {
		h.logger.Error("reserve download", "err", err)
		httperr.Write(w, httperr.ErrInternal)
		return
	}

	body, err := h.artifacts.Open(r.Context(), key, in.Password)
	if err != nil {
		/* Given back, because nothing left the instance. The budget counts
		   bytes that escaped, and an agent that never answered produced none —
		   whereas keeping the charge would mean three unreachable-agent
		   attempts lock the owner out of that artifact for good, exactly while
		   they are fixing the bridge. A retry that DOES produce bytes is
		   charged like any other, so this is not a free loop. */
		if relErr := h.repo.ReleaseDownload(context.WithoutCancel(r.Context()), reservation); relErr != nil {
			h.logger.Warn("release download reservation", "err", relErr)
		}
		h.logger.Error("artifact bridge", "err", err)
		httperr.Write(w, httperr.New(http.StatusBadGateway, "artifact_unavailable",
			"the backup agent could not serve this artifact"))
		return
	}
	defer func() { _ = body.Close() }()

	if h.auditDownload != nil {
		h.auditDownload(r, key)
	}

	/* Headers before the first byte. No Content-Length: the agent streams and
	   the age envelope's size is not the stored object's, so any number here
	   would be a guess the browser would then enforce. */
	filename := path.Base(key)
	if !strings.HasSuffix(filename, ".age") {
		// A plaintext-stored artifact still arrives encrypted, so the name has
		// to say so — a file called ".dump" that age wrote is a file the
		// operator will hand to pg_restore and watch fail.
		filename += ".age"
	}
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Disposition", `attachment; filename="`+filename+`"`)
	w.WriteHeader(http.StatusOK)

	sent, copyErr := io.Copy(w, body)
	if copyErr != nil {
		// The status is already written; the honest signal left is a truncated
		// body, which age's chunk authentication makes unopenable rather than
		// silently short.
		h.logger.Error("artifact stream", "err", copyErr, "bytes", sent)
	}
	// Recorded on a fresh context: the request's is already cancelled when the
	// client disconnects mid-stream, and that is exactly the case worth having
	// in the trail.
	if err := h.repo.CompleteDownload(context.WithoutCancel(r.Context()), reservation, sent); err != nil {
		h.logger.Warn("record download completion", "err", err)
	}
}

// DownloadBudget answers what the caller has left, so the screen can say it
// before the click rather than after.
func (h *Handler) DownloadBudget(w http.ResponseWriter, r *http.Request) {
	if !h.artifacts.Enabled() {
		httperr.Write(w, httperr.ErrNotFound)
		return
	}
	runID, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil || runID <= 0 {
		httperr.Write(w, httperr.New(http.StatusBadRequest, "invalid_run", "run id must be a positive integer"))
		return
	}
	key, err := h.repo.ArtifactKeyForRun(r.Context(), runID)
	if errors.Is(err, ErrRunNotFound) {
		httperr.Write(w, httperr.ErrNotFound)
		return
	}
	if err != nil {
		h.logger.Error("artifact key lookup", "err", err)
		httperr.Write(w, httperr.ErrInternal)
		return
	}
	budget, err := h.repo.DownloadBudgetFor(r.Context(), runID, authctx.MustUser(r.Context()), key)
	if err != nil {
		h.logger.Error("download budget", "err", err)
		httperr.Write(w, httperr.ErrInternal)
		return
	}
	httperr.JSON(w, http.StatusOK, budget)
}

// UserZips lists the per-user archives, which no backup_run row names.
func (h *Handler) UserZips(w http.ResponseWriter, r *http.Request) {
	if !h.artifacts.Enabled() {
		httperr.Write(w, httperr.ErrNotFound)
		return
	}
	listing, err := h.artifacts.List(r.Context(), userZipPrefix)
	if err != nil {
		h.logger.Error("artifact listing", "err", err)
		httperr.Write(w, httperr.New(http.StatusBadGateway, "artifact_unavailable",
			"the backup agent could not list the archives"))
		return
	}
	httperr.JSON(w, http.StatusOK, listing)
}

/*
artifactOwner reports which account a key belongs to, if any.

Per-user ZIPs live under `backups/users/<uid>/…` — the uid IS the key's second
segment (backupagent/userzip.go). Everything else — the dump, the object
mirror — belongs to the whole instance and has no owner to name, which is
exactly why it cannot be handed to an administrator.
*/
func artifactOwner(key string) (authctx.UserID, bool) {
	rest, found := strings.CutPrefix(key, userZipPrefix)
	if !found {
		return 0, false
	}
	seg, _, ok := strings.Cut(rest, "/")
	if !ok || seg == "" {
		return 0, false
	}
	uid, err := strconv.ParseInt(seg, 10, 64)
	if err != nil || uid <= 0 {
		return 0, false
	}
	return authctx.UserID(uid), true
}

/*
mayDownload is the ownership rule, and it is the SECOND layer on purpose.

The route already sits behind an owner-only locked permission, so today no
administrator reaches it at all. This check is what keeps §0's line — "an
administrator never reads another account's rows" — true even if that
permission is ever unlocked, regranted, or the route is reused: a whole-
instance artifact has no owner, so only the instance's owner may take it, and a
per-user ZIP goes to its own account and nobody else.

A guard that depends on a configuration staying a particular way is not a
guard.
*/
func mayDownload(p authctx.Principal, key string) bool {
	if p.Role == authctx.RoleOwner {
		return true
	}
	uid, ok := artifactOwner(key)
	return ok && uid == p.UserID
}

// userZipPrefix mirrors backupagent.userZipKeyPrefix. Restated for the same
// reason as the passphrase floor: separate binaries, and this one only needs
// to name the namespace it is asking about — the agent validates it again.
const userZipPrefix = "backups/users/"
