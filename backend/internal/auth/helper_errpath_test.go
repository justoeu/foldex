package auth

import (
	"context"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"foldex/internal/pkg/authctx"
)

func TestExtractedHelpers_SurfaceTxFailures(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	tx := failTx{}
	hash := []byte("0123456789abcdef0123456789abcdef")

	_, _, _, _, err := lookupConsumedRefreshTx(ctx, tx, hash)
	require.ErrorIs(t, err, errForced)

	_, _, _, _, _, err = lockLiveRefreshTx(ctx, tx, hash)
	require.ErrorIs(t, err, errForced)

	expired, err := expireFamilyIfAbsoluteTx(ctx, tx, "fam", ancient(), time.Hour)
	assert.True(t, expired)
	require.Error(t, err)

	inactive, err := revokeFamilyIfInactiveTx(ctx, tx, "fam", StatusDisabled)
	assert.True(t, inactive)
	require.Error(t, err)

	repo := &Repository{}
	_, err = repo.replayOutsideGraceTx(ctx, tx, "fam", 1)
	require.Error(t, err)

	_, err = repo.graceSiblingTx(ctx, tx, "fam", 1, SessionTTL{Access: time.Minute, Refresh: time.Hour, Absolute: 24 * time.Hour, Grace: time.Second})
	require.Error(t, err)

	err = lockEnrollmentUserTx(ctx, tx, dummyEnrollment(), nil, "enroll")
	require.Error(t, err)

	err = spendEmailFactorEnrollmentTx(ctx, tx, dummyEnrollment(), hash)
	require.Error(t, err)

	err = finishPreAuthEnrollmentTx(ctx, tx, authctx.UserID(1), 1, &PreAuth{Challenge: Challenge{ID: 1}}, sessionIssue{}, "enroll")
	require.Error(t, err)

	_, err = lockUserForAdminEditTx(ctx, tx, authctx.UserID(1))
	require.Error(t, err)

	_, err = applyUserUpdateTx(ctx, tx, authctx.UserID(1), nil, nil, nil)
	require.Error(t, err)

	err = lockPasswordSetTx(ctx, tx, authctx.UserID(1), 0, 1)
	require.Error(t, err)

	err = consumeFactorIfPresentTx(ctx, tx, authctx.UserID(1), SecondFactorProof{})
	require.Error(t, err)

	_, _, _, err = lookupLiveInviteTx(ctx, tx, inviteBy{})
	require.Error(t, err)

	pw := "x"
	_, err = insertAcceptedUserTx(ctx, tx, "a@b.c", "a@b.c", "n", inviteCredential{passwordHash: &pw}, authctx.RoleEditor)
	require.Error(t, err)
}

func TestExtractedHelpers_SurfaceCommitFailures(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	tx := commitFailTx{}

	expired, err := expireFamilyIfAbsoluteTx(ctx, tx, "fam", ancient(), time.Hour)
	assert.True(t, expired)
	require.ErrorIs(t, err, errForced)

	inactive, err := revokeFamilyIfInactiveTx(ctx, tx, "fam", StatusDisabled)
	assert.True(t, inactive)
	require.ErrorIs(t, err, errForced)
}

func TestExtractedHelpers_WalkEachTxStep(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	hash := []byte("0123456789abcdef0123456789abcdef")
	pw := "x"
	ttl := SessionTTL{Access: time.Minute, Refresh: time.Hour, Absolute: 24 * time.Hour, Grace: time.Second}
	repo := &Repository{}
	walk := func(fn func(pgx.Tx)) {
		t.Helper()
		for n := 1; n <= 12; n++ {
			fn(&seqTx{failAt: n})
			fn(&seqTx{failAt: n, zeroRows: true})
		}
	}
	walk(func(tx pgx.Tx) { _, _, _, _, _ = lookupConsumedRefreshTx(ctx, tx, hash) })
	walk(func(tx pgx.Tx) { _, _, _, _, _, _ = lockLiveRefreshTx(ctx, tx, hash) })
	walk(func(tx pgx.Tx) { _, _ = expireFamilyIfAbsoluteTx(ctx, tx, "fam", ancient(), time.Hour) })
	walk(func(tx pgx.Tx) { _, _ = revokeFamilyIfInactiveTx(ctx, tx, "fam", StatusDisabled) })
	walk(func(tx pgx.Tx) { _, _ = repo.replayOutsideGraceTx(ctx, tx, "fam", 1) })
	walk(func(tx pgx.Tx) { _, _ = repo.graceSiblingTx(ctx, tx, "fam", 1, ttl) })
	walk(func(tx pgx.Tx) { _ = lockEnrollmentUserTx(ctx, tx, dummyEnrollment(), nil, "enroll") })
	walk(func(tx pgx.Tx) { _ = spendEmailFactorEnrollmentTx(ctx, tx, dummyEnrollment(), hash) })
	walk(func(tx pgx.Tx) {
		_ = finishPreAuthEnrollmentTx(ctx, tx, authctx.UserID(1), 1, &PreAuth{Challenge: Challenge{ID: 1}}, sessionIssue{}, "enroll")
	})
	walk(func(tx pgx.Tx) { _, _ = lockUserForAdminEditTx(ctx, tx, authctx.UserID(1)) })
	walk(func(tx pgx.Tx) { _, _ = applyUserUpdateTx(ctx, tx, authctx.UserID(1), nil, nil, nil) })
	walk(func(tx pgx.Tx) { _ = lockPasswordSetTx(ctx, tx, authctx.UserID(1), 0, 1) })
	walk(func(tx pgx.Tx) {
		_ = consumeFactorIfPresentTx(ctx, tx, authctx.UserID(1), SecondFactorProof{Method: "totp"})
	})
	walk(func(tx pgx.Tx) { _, _, _, _ = lookupLiveInviteTx(ctx, tx, inviteBy{}) })
	walk(func(tx pgx.Tx) {
		_, _ = insertAcceptedUserTx(ctx, tx, "a@b.c", "a@b.c", "n", inviteCredential{passwordHash: &pw}, authctx.RoleEditor)
	})
	walk(func(tx pgx.Tx) {
		_, _ = loadPreviousChallengeTx(ctx, tx, NewChallenge{UserID: authctx.UserID(1), Purpose: PurposeTOTP, TokenVersion: 1})
	})
	walk(func(tx pgx.Tx) { _ = inheritChallengeCodesTx(ctx, tx, 7, 8) })
	walk(func(tx pgx.Tx) { _, _ = diagnoseChallengeSendTx(ctx, tx, 1, time.Minute) })
	walk(func(tx pgx.Tx) { _ = lockChallengeUser(ctx, tx, 1) })
	walk(func(tx pgx.Tx) {
		_ = confirmTOTPRowTx(ctx, tx, authctx.UserID(1), 1, 1, TOTPProof{})
	})
	walk(func(tx pgx.Tx) { _ = consumeTOTPProofTx(ctx, tx, authctx.UserID(1), TOTPProof{}) })
	walk(func(tx pgx.Tx) { _ = replaceRecoveryCodesTx(ctx, tx, authctx.UserID(1), [][]byte{[]byte("ab")}) })
	walk(func(tx pgx.Tx) { _, _ = lockConvertUserTx(ctx, tx, 1, 1, "hash") })
	walk(func(tx pgx.Tx) { _, _, _, _ = consumeConvertChallengeTx(ctx, tx, 1, 1, 1) })
	ph := "hash"
	walk(func(tx pgx.Tx) { _, _ = retirePasswordTx(ctx, tx, 1, 1, &ph) })
	walk(func(tx pgx.Tx) { _, _, _, _, _ = loadLiveEmailChangeTx(ctx, tx, hash) })
	walk(func(tx pgx.Tx) { _ = lockEmailChangeTargetTx(ctx, tx, 1, 1) })
	sid := int64(1)
	walk(func(tx pgx.Tx) { _ = requireLiveEmailChangeSessionTx(ctx, tx, &sid) })
	proof := TOTPProof{Counter: 1, Ciphertext: hash, Nonce: hash[:12]}
	walk(func(tx pgx.Tx) {
		_, _ = consumeChallengeProofTx(ctx, tx, Challenge{ID: 1, UserID: 1}, challengeProof{totp: &proof})
	})
	walk(func(tx pgx.Tx) {
		_, _ = consumeChallengeProofTx(ctx, tx, Challenge{ID: 1, UserID: 1}, challengeProof{emailDigest: hash})
	})
	walk(func(tx pgx.Tx) {
		_, _ = consumeChallengeProofTx(ctx, tx, Challenge{ID: 1, UserID: 1}, challengeProof{recoveryDigest: hash})
	})
	walk(func(tx pgx.Tx) { _, _ = consumeTOTPProofIfCurrentTx(ctx, tx, 1, proof) })
	walk(func(tx pgx.Tx) {
		_ = consumeSecondFactorTx(ctx, tx, 1, SecondFactorProof{Method: MethodTOTP, TOTP: &proof})
	})
	walk(func(tx pgx.Tx) {
		_ = consumeSecondFactorTx(ctx, tx, 1, SecondFactorProof{Method: MethodRecovery, Digest: hash})
	})
	walk(func(tx pgx.Tx) {
		_ = consumeSecondFactorTx(ctx, tx, 1, SecondFactorProof{Method: MethodEmailOTP, Digest: hash})
	})
	walk(func(tx pgx.Tx) { _ = consumeSingleUseTx(ctx, tx, "update x", 1) })
	walk(func(tx pgx.Tx) { _ = disableFactorTx(ctx, tx, 1, 1, 1, disableKindTOTP) })
	walk(func(tx pgx.Tx) { _ = disableFactorTx(ctx, tx, 1, 1, 1, disableKindEmail) })
	walk(func(tx pgx.Tx) { _ = guardLastAdminTx(ctx, tx, 1) })
	issue := sessionIssue{hashes: issuedSessionHashes{access: hash, refresh: hash, csrf: hash}}
	walk(func(tx pgx.Tx) { _, _ = issueSessionTx(ctx, tx, 1, issue, "127.0.0.1", "ua") })
	walk(func(tx pgx.Tx) { _, _ = insertSessionTx(ctx, tx, 1, issue, "fam", nil, nil, nil) })
	walk(func(tx pgx.Tx) { _ = requireLiveSessionTx(ctx, tx, 1, 1) })
}

func TestChallengeIdentityArgs_NilAndSet(t *testing.T) {
	t.Parallel()
	p, s, e := challengeIdentityArgs(nil)
	assert.Nil(t, p)
	assert.Nil(t, s)
	assert.Nil(t, e)
	gotP, gotS, gotE := challengeIdentityArgs(&linkedIdentity{provider: "google", subject: "sub", email: "a@b.c"})
	require.NotNil(t, gotP)
	assert.Equal(t, "google", *gotP)
	assert.Equal(t, "sub", *gotS)
	assert.Equal(t, "a@b.c", *gotE)
}

func TestTryStepUpProof_RejectsUnknownShapes(t *testing.T) {
	t.Parallel()
	h := &Handler{}
	_, err := h.tryStepUpProof(context.Background(), authctx.UserID(1), User{}, "nope")
	assert.ErrorIs(t, err, ErrBadCredentials)
	_, err = h.tryNumericStepUpProof(context.Background(), authctx.UserID(1), User{}, "123456")
	assert.ErrorIs(t, err, ErrBadCredentials)
}

func TestRequireLiveEmailChangeSession_NilIsOK(t *testing.T) {
	t.Parallel()
	require.NoError(t, requireLiveEmailChangeSessionTx(context.Background(), failTx{}, nil))
}

func TestScanAuditHelpers_SurfaceRowFailures(t *testing.T) {
	t.Parallel()
	rows := &failRows{err: errForced}

	_, err := scanAuditDayBuckets(rows)
	require.ErrorIs(t, err, errForced)
	rows = &failRows{err: errForced}
	_, err = scanAuditDistribution(rows)
	require.ErrorIs(t, err, errForced)
	rows = &failRows{err: errForced}
	_, err = scanAuditActors(rows)
	require.ErrorIs(t, err, errForced)
	rows = &failRows{err: errForced}
	_, err = scanAuditOrigins(rows)
	require.ErrorIs(t, err, errForced)
}
