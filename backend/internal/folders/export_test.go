package folders

// Test seams for the bcrypt TOCTOU windows on unlock and master-password reset.
func SetAfterUnlockProof(fn func()) { afterUnlockProof = fn }
func SetAfterMasterProof(fn func()) { afterMasterProof = fn }
