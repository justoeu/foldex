package auth

import (
	"context"

	"foldex/internal/pkg/authctx"
)

// SetTOTPVerificationHookForTest installs a per-handler synchronization point
// after cryptographic verification and before proof consumption.
func SetTOTPVerificationHookForTest(h *Handler, hook func(context.Context, authctx.UserID, TOTPProof)) {
	h.afterTOTPVerification = hook
}
