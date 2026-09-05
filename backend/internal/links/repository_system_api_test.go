package links

import (
	"context"
	"testing"
	"time"
)

// Compile fixture for BP-MEN-001: Find is a lying name because the body is an
// UPDATE … SKIP LOCKED claim. A listing helper must not compile against it.
func TestSystemClaimDueForCheck_IsTheExportedSweep(t *testing.T) {
	var _ interface {
		SystemClaimDueForCheck(context.Context, int) ([]DueLink, error)
	} = (*Repository)(nil)
}

// Compile fixture for BP-MEN-002: the four optional preview columns travel as
// named fields so Error cannot compile into Description's slot.
func TestPreviewPatch_NamedFieldsAreTheUpdateAPI(t *testing.T) {
	var _ interface {
		SystemUpdatePreview(context.Context, int64, PreviewStatus, PreviewPatch) error
		SystemUpdatePreviewIfUnchanged(context.Context, int64, time.Time, int64, PreviewStatus, PreviewPatch) (bool, error)
	} = (*Repository)(nil)
	patch := PreviewPatch{Error: new(string)}
	if patch.Favicon != nil || patch.OGImage != nil || patch.Description != nil || patch.Error == nil {
		t.Fatal("named Error must stay distinct from Favicon, OGImage and Description")
	}
}
