package notes_test

import (
	"context"
	"math"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/stretchr/testify/assert"

	"foldex/internal/notes"
	"foldex/internal/pkg/domainerr"
)

type fakePublicNoteResolver struct {
	byID   map[int64]notes.Note
	bySlug map[string]notes.Note
}

func (f *fakePublicNoteResolver) SystemViewAndResolveByID(_ context.Context, id int64) (notes.Note, error) {
	if note, ok := f.byID[id]; ok {
		return note, nil
	}
	return notes.Note{}, domainerr.ErrNotFound
}

func (f *fakePublicNoteResolver) SystemViewAndResolveBySlug(_ context.Context, slug string) (notes.Note, error) {
	if note, ok := f.bySlug[slug]; ok {
		return note, nil
	}
	return notes.Note{}, domainerr.ErrNotFound
}

func TestPublicHandler_PublicIdentifierClassification(t *testing.T) {
	const (
		maxInt64 = "9223372036854775807"
		overflow = "18446744073709551617"
	)
	tests := []struct {
		name            string
		raw             string
		allowNumericIDs bool
		wantStatus      int
	}{
		{name: "max int64 feature on", raw: maxInt64, allowNumericIDs: true, wantStatus: http.StatusOK},
		{name: "max int64 feature off", raw: maxInt64, allowNumericIDs: false, wantStatus: http.StatusNotFound},
		{name: "overflow feature on is slug", raw: overflow, allowNumericIDs: true, wantStatus: http.StatusOK},
		{name: "overflow feature off is slug", raw: overflow, allowNumericIDs: false, wantStatus: http.StatusOK},
		{name: "zero feature on is slug", raw: "0", allowNumericIDs: true, wantStatus: http.StatusOK},
		{name: "zero feature off is slug", raw: "0", allowNumericIDs: false, wantStatus: http.StatusOK},
		{name: "signed feature on is slug", raw: "+42", allowNumericIDs: true, wantStatus: http.StatusOK},
		{name: "signed feature off is slug", raw: "+42", allowNumericIDs: false, wantStatus: http.StatusOK},
		{name: "numeric-looking slug feature on", raw: "42-notes", allowNumericIDs: true, wantStatus: http.StatusOK},
		{name: "numeric-looking slug feature off", raw: "42-notes", allowNumericIDs: false, wantStatus: http.StatusOK},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			note := notes.Note{Title: "Public", BodyHTML: "<p>body</p>"}
			repo := &fakePublicNoteResolver{bySlug: map[string]notes.Note{tt.raw: note}}
			if tt.raw == maxInt64 {
				repo.byID = map[int64]notes.Note{math.MaxInt64: note}
			}
			r := chi.NewRouter()
			notes.NewPublicHandler(repo, tt.allowNumericIDs).Mount(r)

			rec := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodGet, "/n/"+tt.raw, nil)
			r.ServeHTTP(rec, req)
			assert.Equal(t, tt.wantStatus, rec.Code)
		})
	}
}
