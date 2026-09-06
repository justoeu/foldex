package folders

import "errors"

var (
	ErrLocked              = errors.New("this folder is password-protected")
	ErrWrongPassword       = errors.New("current password is required to change or remove an existing password")
	ErrHintWithoutPassword = errors.New("cannot set a password hint on a folder without a password")
	ErrHintMatchesPassword = errors.New("password hint must not be the same as the password")
	ErrParentCycle         = errors.New("parent_id would create a folder cycle")
	ErrDescendantProtected = errors.New("folder subtree contains password-protected descendants")
	ErrGateUnavailable     = errors.New("folder content gate is not configured")
	ErrMasterNotConfigured = errors.New("no master password is configured")
	ErrWrongMasterPassword = errors.New("incorrect master password")
	ErrStaleMasterProof    = errors.New("master password changed during reset")
	ErrPasswordChanged     = errors.New("folder password changed during unlock")
)
