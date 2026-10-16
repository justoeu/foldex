package auth

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"foldex/internal/auth/anomaly"
	"foldex/internal/mailer"
	"foldex/internal/pkg/authctx"
	"foldex/internal/testsupport"
)

// Every Repository method must surface an infrastructure failure as a
// wrapped error (§7) — never a panic, never a silent zero value. A closed
// pool makes the first database touch of each method fail deterministically
// without a container, which exercises exactly the error branches the
// integration suite (happy-path against a live database) cannot reach.

func err2[T any](_ T, err error) error { return err }
func err3[T, U any](_ T, _ U, err error) error {
	return err
}

func TestRepositoryClosedPoolErrors(t *testing.T) {
	repo := NewRepository(testsupport.ClosedPool(t))
	ctx := context.Background()
	uid := authctx.UserID(1)
	sp := SessionProof{UserID: uid, SessionID: 1, TokenVersion: 1}
	ttl := SessionTTL{Access: time.Minute, Refresh: time.Hour}
	proof := SecondFactorProof{Method: MethodRecovery, Digest: []byte{1, 2, 3}}
	enroll := EnrollmentComplete{
		UID: uid, TokenVersion: 1,
		RecoveryHashes: [][]byte{{9}},
		Session:        LiveSession{ID: 1},
	}

	cases := map[string]func() error{
		"EnqueueMail":    func() error { return repo.EnqueueMail(ctx, mailer.Envelope{}, "en") },
		"GetUser":        func() error { return err2(repo.GetUser(ctx, uid)) },
		"ListUsers":      func() error { return err2(repo.ListUsers(ctx, 10, 0)) },
		"verifyPassword": func() error { return err3(repo.verifyPassword(ctx, "a@example.com", "pw")) },
		"loginBucketKey": func() error { return err2(repo.loginBucketKey(ctx, "a@example.com")) },
		"NeedsBootstrap": func() error { return err2(repo.NeedsBootstrap(ctx)) },
		"Bootstrap": func() error {
			return err2(repo.Bootstrap(ctx, "owner@example.com", "Owner", "correct horse battery staple"))
		},
		"SetPassword": func() error { return repo.SetPassword(ctx, sp, "correct horse battery staple", proof) },
		"ChangePassword": func() error {
			return repo.ChangePassword(ctx, uid, 1, "old-password-123", "correct horse battery staple")
		},
		"VerifyUserPasswordEpoch": func() error {
			return err2(repo.VerifyUserPasswordEpoch(ctx, uid, "correct horse battery staple"))
		},
		"UpdateOwnProfile": func() error {
			name := "New Name"
			return err2(repo.UpdateOwnProfile(ctx, uid, &name, nil, nil, nil))
		},
		"UpdateUser": func() error {
			name := "New Name"
			return err2(repo.UpdateUser(ctx, uid, &name, nil, nil))
		},
		"DeleteUser":        func() error { return repo.DeleteUser(ctx, uid) },
		"TransferOwnership": func() error { return err2(repo.TransferOwnership(ctx, uid, 2)) },
		"CreateInvite": func() error {
			return err3(repo.CreateInvite(ctx, "invitee@example.com", authctx.RoleViewer, uid, time.Hour, MailDraft{}))
		},
		"LookupInvite": func() error { return err2(repo.LookupInvite(ctx, "raw-token")) },
		"ListInvites":  func() error { return err2(repo.ListInvites(ctx, 10)) },
		"RevokeInvite": func() error { return repo.RevokeInvite(ctx, 1) },
		"AcceptInvite": func() error { return err2(repo.AcceptInvite(ctx, "raw-token", "Name", "correct horse battery staple")) },
		"AcceptInviteWithIdentityByID": func() error {
			return err2(repo.AcceptInviteWithIdentityByID(ctx, 1, "Name", "google", "subj", "a@example.com"))
		},
		"ResolveAccess": func() error { return err2(repo.ResolveAccess(ctx, "raw-token")) },
		"IssueSession":  func() error { return err3(repo.IssueSession(ctx, uid, 1, ttl, "127.0.0.1", "ua")) },
		"ListSessions":  func() error { return err2(repo.ListSessions(ctx, uid, 1)) },
		"RevokeSession": func() error { return repo.RevokeSession(ctx, uid, 1, "test") },
		"RevokeFamilyByTokens": func() error {
			return repo.RevokeFamilyByTokens(ctx, "access", "refresh", "test")
		},
		"RevokeAllForUser": func() error { return repo.RevokeAllForUser(ctx, uid, "test") },
		"Sweep":            func() error { return err2(repo.Sweep(ctx, time.Hour)) },
		"AdminCreateUser": func() error {
			return err2(repo.AdminCreateUser(ctx, NewUser{
				Email: "new@example.com", Name: "New",
				Password: "correct horse battery staple", Role: authctx.RoleViewer,
			}))
		},
		"UsernameAvailable": func() error { return err2(repo.UsernameAvailable(ctx, uid, "name")) },
		"EmailAvailable":    func() error { return err3(repo.EmailAvailable(ctx, "a@example.com")) },
		"CreateChallenge": func() error {
			return err3(repo.CreateChallenge(ctx, NewChallenge{
				UserID: uid, Purpose: PurposeTOTP, TokenVersion: 1, TTL: time.Minute,
			}))
		},
		"DiagnoseChallenge": func() error { return err2(repo.DiagnoseChallenge(ctx, "raw-token")) },
		"ResolveChallenge":  func() error { return err2(repo.ResolveChallenge(ctx, "raw-token", PurposeTOTP)) },
		"BumpChallengeAttempt": func() error {
			return err2(repo.BumpChallengeAttempt(ctx, 1))
		},
		"ConsumeChallenge": func() error { return repo.ConsumeChallenge(ctx, 1) },
		"CreateChallengeEmailOTP": func() error {
			return err2(repo.CreateChallengeEmailOTP(ctx, 1, []byte{1}, time.Minute, time.Second, MailDraft{}))
		},
		"StartTOTPEnrollment": func() error {
			return repo.StartTOTPEnrollment(ctx, sp, []byte{1}, []byte{2})
		},
		"LoadTOTPSecret": func() error { return err2(repo.LoadTOTPSecret(ctx, uid)) },
		"HasConfirmedSecondFactor": func() error {
			return err2(repo.HasConfirmedSecondFactor(ctx, uid, false))
		},
		"CompleteTOTPEnrollment": func() error {
			return err3(repo.CompleteTOTPEnrollment(ctx, enroll,
				TOTPProof{Counter: 1, Ciphertext: []byte{1}, Nonce: []byte{2}}))
		},
		"Complete2FA": func() error {
			ch := Challenge{ID: 1, UserID: uid, Purpose: PurposeTOTP, TokenVersion: 1}
			p := challengeProof{totp: &TOTPProof{Counter: 1, Ciphertext: []byte{1}, Nonce: []byte{2}}}
			return err3(repo.Complete2FA(ctx, ch, p, ttl, "127.0.0.1", "ua"))
		},
		"ConsumeSecondFactor": func() error { return repo.ConsumeSecondFactor(ctx, uid, proof) },
		"DisableTOTP": func() error {
			return repo.DisableTOTP(ctx, sp, "correct horse battery staple", proof)
		},
		"RegenerateRecoveryCodes": func() error {
			return repo.RegenerateRecoveryCodes(ctx, sp, "correct horse battery staple", proof, [][]byte{{1}})
		},
		"CountRecoveryCodes": func() error { return err2(repo.CountRecoveryCodes(ctx, uid)) },
		"CreateEmailVerification": func() error {
			return err2(repo.CreateEmailVerification(ctx, uid, time.Minute, time.Second, MailDraft{}))
		},
		"UserForPasswordReset": func() error {
			return err3(repo.UserForPasswordReset(ctx, "a@example.com"))
		},
		"CreatePasswordReset": func() error {
			return err2(repo.CreatePasswordReset(ctx, uid, time.Minute, "127.0.0.1", MailDraft{}))
		},
		"CreateAdminPasswordRecovery": func() error {
			return repo.CreateAdminPasswordRecovery(ctx, uid, time.Minute,
				func(string, string) MailDraft { return MailDraft{} })
		},
		"ConsumePasswordReset": func() error {
			return err2(repo.ConsumePasswordReset(ctx, "raw-token", "correct horse battery staple"))
		},
		"ConsumeEmailVerification": func() error {
			return err2(repo.ConsumeEmailVerification(ctx, []byte{1}))
		},
		"SweepTwoFactor": func() error { return err2(repo.SweepTwoFactor(ctx, time.Hour)) },
		"Anomalies": func() error {
			return err2(repo.Anomalies(ctx, time.Hour, anomaly.Thresholds{SprayAccounts: 3, HammerFailures: 5, WindowMinutes: 60}))
		},
		"AbuseObservedSince": func() error { return err2(repo.AbuseObservedSince(ctx)) },
		"CreateAPIToken":     func() error { return err2(repo.CreateAPIToken(ctx, uid, "cli", time.Hour)) },
		"ResolveAPIToken":    func() error { return err2(repo.ResolveAPIToken(ctx, "presented")) },
		"ListAPITokens":      func() error { return err2(repo.ListAPITokens(ctx, uid)) },
		"RevokeAPIToken":     func() error { return repo.RevokeAPIToken(ctx, uid, 1) },
		"SweepAPITokens":     func() error { return err2(repo.SweepAPITokens(ctx, time.Hour)) },
		"RequestEmailChange": func() error {
			return err2(repo.RequestEmailChange(ctx, uid, 1, "new@example.com", time.Minute,
				func(string) MailDraft { return MailDraft{} },
				func(string, string) MailDraft { return MailDraft{} }))
		},
		"ConsumeEmailChange":    func() error { return err2(repo.ConsumeEmailChange(ctx, []byte{1})) },
		"PendingEmailChangeFor": func() error { return err2(repo.PendingEmailChangeFor(ctx, uid)) },
		"CancelEmailChange":     func() error { return repo.CancelEmailChange(ctx, uid) },
		"StartEmailFactorEnrollment": func() error {
			return repo.StartEmailFactorEnrollment(ctx, sp,
				EmailOTPMint{Digest: []byte{1}, TTL: time.Minute, Cooldown: time.Second})
		},
		"CompleteEmailFactorEnrollment": func() error {
			return err3(repo.CompleteEmailFactorEnrollment(ctx, enroll, []byte{1}))
		},
		"DisableEmailFactor": func() error { return repo.DisableEmailFactor(ctx, sp, proof) },
		"CreateStepUpEmailOTP": func() error {
			return repo.CreateStepUpEmailOTP(ctx, sp,
				EmailOTPMint{Digest: []byte{1}, TTL: time.Minute, Cooldown: time.Second})
		},
		"StepUpEmailOTPIsLive": func() error { return err2(repo.StepUpEmailOTPIsLive(ctx, uid, []byte{1})) },
		"RecoveryCodeIsLive":   func() error { return err2(repo.RecoveryCodeIsLive(ctx, uid, []byte{1})) },
		"BlockIP":              func() error { return err2(repo.BlockIP(ctx, "127.0.0.1", "test", &uid, "a@example.com")) },
		"UnblockIP":            func() error { return err2(repo.UnblockIP(ctx, "127.0.0.1")) },
		"ListIPBlocks":         func() error { return err2(repo.ListIPBlocks(ctx)) },
		"BlockedIPs":           func() error { return err2(repo.BlockedIPs(ctx)) },
		"CreateOAuthState": func() error {
			return err2(repo.CreateOAuthState(ctx, "google", "login", "verifier", nil, nil, time.Minute))
		},
		"CreateOAuthLinkState": func() error {
			return err2(repo.CreateOAuthLinkState(ctx, "google", "verifier", sp, time.Minute))
		},
		"ConsumeOAuthState": func() error { return err2(repo.ConsumeOAuthState(ctx, "raw-state")) },
		"SweepOAuthState":   func() error { return err2(repo.SweepOAuthState(ctx, time.Hour)) },
		"UserByIdentity":    func() error { return err2(repo.UserByIdentity(ctx, "google", "subj")) },
		"UserByEmail":       func() error { return err2(repo.UserByEmail(ctx, "a@example.com")) },
		"ListIdentities":    func() error { return err2(repo.ListIdentities(ctx, uid)) },
		"ValidateOAuthLinkProof": func() error {
			return repo.ValidateOAuthLinkProof(ctx, OAuthState{ID: 1}, "raw-access", time.Minute)
		},
		"LinkIdentity": func() error {
			return repo.LinkIdentity(ctx, OAuthState{ID: 1}, "raw-access", "google", "subj", "a@example.com", time.Minute)
		},
		"ConvertToProvider": func() error {
			return err3(repo.ConvertToProvider(ctx, 1, "correct horse battery staple"))
		},
		"UnlinkIdentity": func() error {
			return repo.UnlinkIdentity(ctx, sp, "google", "correct horse battery staple")
		},
		"ProvisionOAuthUser": func() error {
			return err2(repo.ProvisionOAuthUser(ctx, "a@example.com", "Name", "google", "subj", authctx.RoleViewer))
		},
		"ListAudit":       func() error { return err2(repo.ListAudit(ctx, AuditFilter{})) },
		"ListOwnActivity": func() error { return err2(repo.ListOwnActivity(ctx, uid, 0, nil, 50)) },
	}

	for name, call := range cases {
		t.Run(name, func(t *testing.T) {
			require.Error(t, call(), "%s must surface the closed-pool failure", name)
		})
	}

	// Fire-and-forget touches log-and-move-on by design; they must not panic
	// when the pool is gone.
	require.NotPanics(t, func() {
		repo.TouchSession(ctx, 1)
		repo.TouchAPIToken(ctx, 1)
		repo.TouchIdentity(ctx, "google", "subj")
	})
}
