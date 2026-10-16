package folders

import (
	"context"

	"foldex/internal/pkg/authctx"
)

// Test seams for the bcrypt TOCTOU windows on unlock and master-password reset.
func SetAfterUnlockProof(fn func()) { afterUnlockProof = fn }
func SetAfterMasterProof(fn func()) { afterMasterProof = fn }

// DeleteCascadeForTest exposes the handler's cascade path to integration
// tests. Production reaches deleteCascade only through DELETE ?cascade=1,
// so no exported repository wrapper ships for it.
func (r *Repository) DeleteCascadeForTest(ctx context.Context, uid authctx.UserID, id int64, unlockKey []byte, unlockToken string) error {
	_, err := r.deleteCascade(ctx, uid, id, unlockKey, unlockToken)
	return err
}
