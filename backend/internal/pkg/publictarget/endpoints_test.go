package publictarget_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/stretchr/testify/assert"

	"foldex/internal/notes"
	"foldex/internal/redirect"
)

type endpointLinkResolver struct {
	call string
}

func (r *endpointLinkResolver) ClickAndResolve(_ context.Context, _ int64) (string, error) {
	r.call = "id"
	return "https://example.com/id", nil
}

func (r *endpointLinkResolver) ClickAndResolveBySlug(_ context.Context, _ string) (string, error) {
	r.call = "slug"
	return "https://example.com/slug", nil
}

type endpointNoteResolver struct {
	call string
}

func (r *endpointNoteResolver) SystemViewAndResolveByID(_ context.Context, _ int64) (notes.Note, error) {
	r.call = "id"
	return notes.Note{Title: "By ID", BodyHTML: "<p>body</p>"}, nil
}

func (r *endpointNoteResolver) SystemViewAndResolveBySlug(_ context.Context, _ string) (notes.Note, error) {
	r.call = "slug"
	return notes.Note{Title: "By slug", BodyHTML: "<p>body</p>"}, nil
}

func TestPublicEndpointsShareTheTargetResolutionMatrix(t *testing.T) {
	tests := []struct {
		name            string
		raw             string
		allowNumericIDs bool
		wantCall        string
		wantLinkStatus  int
		wantNoteStatus  int
	}{
		{name: "numeric enabled", raw: "42", allowNumericIDs: true, wantCall: "id", wantLinkStatus: http.StatusFound, wantNoteStatus: http.StatusOK},
		{name: "numeric disabled", raw: "42", wantLinkStatus: http.StatusNotFound, wantNoteStatus: http.StatusNotFound},
		{name: "max int64 enabled", raw: "9223372036854775807", allowNumericIDs: true, wantCall: "id", wantLinkStatus: http.StatusFound, wantNoteStatus: http.StatusOK},
		{name: "max int64 disabled", raw: "9223372036854775807", wantLinkStatus: http.StatusNotFound, wantNoteStatus: http.StatusNotFound},
		{name: "overflow enabled", raw: "9223372036854775808", allowNumericIDs: true, wantCall: "slug", wantLinkStatus: http.StatusFound, wantNoteStatus: http.StatusOK},
		{name: "overflow disabled", raw: "9223372036854775808", wantCall: "slug", wantLinkStatus: http.StatusFound, wantNoteStatus: http.StatusOK},
		{name: "zero enabled", raw: "0", allowNumericIDs: true, wantCall: "slug", wantLinkStatus: http.StatusFound, wantNoteStatus: http.StatusOK},
		{name: "zero disabled", raw: "0", wantCall: "slug", wantLinkStatus: http.StatusFound, wantNoteStatus: http.StatusOK},
		{name: "positive signed enabled", raw: "+42", allowNumericIDs: true, wantCall: "slug", wantLinkStatus: http.StatusFound, wantNoteStatus: http.StatusOK},
		{name: "positive signed disabled", raw: "+42", wantCall: "slug", wantLinkStatus: http.StatusFound, wantNoteStatus: http.StatusOK},
		{name: "negative signed enabled", raw: "-42", allowNumericIDs: true, wantCall: "slug", wantLinkStatus: http.StatusFound, wantNoteStatus: http.StatusOK},
		{name: "negative signed disabled", raw: "-42", wantCall: "slug", wantLinkStatus: http.StatusFound, wantNoteStatus: http.StatusOK},
		{name: "numeric prefix slug enabled", raw: "42-notes", allowNumericIDs: true, wantCall: "slug", wantLinkStatus: http.StatusFound, wantNoteStatus: http.StatusOK},
		{name: "numeric prefix slug disabled", raw: "42-notes", wantCall: "slug", wantLinkStatus: http.StatusFound, wantNoteStatus: http.StatusOK},
		{name: "ordinary slug enabled", raw: "release-notes", allowNumericIDs: true, wantCall: "slug", wantLinkStatus: http.StatusFound, wantNoteStatus: http.StatusOK},
		{name: "ordinary slug disabled", raw: "release-notes", wantCall: "slug", wantLinkStatus: http.StatusFound, wantNoteStatus: http.StatusOK},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			linkRepo := &endpointLinkResolver{}
			noteRepo := &endpointNoteResolver{}
			router := chi.NewRouter()
			redirect.NewHandler(linkRepo, tt.allowNumericIDs).Mount(router)
			notes.NewPublicHandler(noteRepo, tt.allowNumericIDs).Mount(router)

			linkRec := httptest.NewRecorder()
			router.ServeHTTP(linkRec, httptest.NewRequest(http.MethodGet, "/go/"+tt.raw, nil))
			noteRec := httptest.NewRecorder()
			router.ServeHTTP(noteRec, httptest.NewRequest(http.MethodGet, "/n/"+tt.raw, nil))

			assert.Equal(t, tt.wantLinkStatus, linkRec.Code)
			assert.Equal(t, tt.wantNoteStatus, noteRec.Code)
			assert.Equal(t, tt.wantCall, linkRepo.call)
			assert.Equal(t, tt.wantCall, noteRepo.call)
		})
	}
}
