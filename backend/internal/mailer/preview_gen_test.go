//go:build previewgen

package mailer

import (
	"os"
	"path/filepath"
	"testing"
)

// A generator, not a test — it asserts nothing beyond "everything rendered".
//
// It exists because these eleven documents are looked at far more often than
// they are changed, and the alternative to a generator is sending yourself real
// mail to see a spacing tweak. Behind a build tag so it never runs in the
// suite, and behind an explicit output directory so it cannot scatter files
// into whatever directory someone happened to run `go test` from.
//
//	PREVIEW_OUT=/tmp/mail PREVIEW_LOCALE=pt \
//	  go test -tags previewgen ./internal/mailer/ -run TestGeneratePreview
//
// The values below are deliberately realistic: a token of the length the real
// one has, a name with a space in it, a plausible remaining-codes count. A
// preview built from "test" and "123" hides exactly the wrapping and overflow
// problems it is supposed to reveal.
func TestGeneratePreview(t *testing.T) {
	out := os.Getenv("PREVIEW_OUT")
	if out == "" {
		t.Skip("set PREVIEW_OUT to a directory to render the previews")
	}
	locale := os.Getenv("PREVIEW_LOCALE")
	if locale == "" {
		locale = DefaultLocale
	}
	envs := map[string]Envelope{
		"invite":             InviteMessage("grace@x.test", "Ana Souza", "https://foldex.exemplo/#invite=T0k3nDeConvite81bMA8A0v3", 48),
		"password_reset":     PasswordResetMessage("grace@x.test", "https://foldex.exemplo/#reset=81bMA8A0v3ISa6bFEcNlREtjL1aO-cwV2Ww5rDnCtsA", 30),
		"reset_unavailable":  PasswordResetUnavailableMessage("grace@x.test"),
		"login_code":         LoginCodeMessage("grace@x.test", "492817", 10),
		"enroll_email_2fa":   EnrollEmail2FAMessage("grace@x.test", "308114", 10),
		"step_up_code":       StepUpCodeMessage("grace@x.test", "771203", 10),
		"verify_email":       VerifyEmailMessage("grace@x.test", "https://foldex.exemplo/#verify=Xc9vB2nQ7pLm4KsT", 60),
		"admin_recovery":     AdminPasswordRecoveryMessage("grace@x.test", "https://foldex.exemplo/#reset=Rec0v3ryT0k3nAdm", 30),
		"session_revoked":    SessionRevokedMessage("grace@x.test"),
		"recovery_code_used": RecoveryCodeUsedMessage("grace@x.test", 7),
		"account_converted":  AccountConvertedMessage("grace@x.test", "grace@gmail.com"),
	}
	// Every shipped message, not a hand-picked few: a layout nobody previews is
	// a layout nobody notices breaking.
	for _, name := range TemplateNames() {
		if _, ok := envs[name]; !ok {
			t.Fatalf("message %q ships but has no preview envelope — add one", name)
		}
	}
	for name, env := range envs {
		m, err := Render(env, locale)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		for ext, content := range map[string]string{
			"html": m.HTML, "txt": m.Text, "subject": m.Subject,
		} {
			// 0600, the same floor this project uses for .env, the VAPID key and
			// the auth keyfile. A rendered preview contains a full reset link —
			// synthetic here, and the values above must STAY synthetic — but the
			// output directory is whatever the operator names, and a shared /tmp
			// on a multi-user box should not hand them to everyone.
			if err := os.WriteFile(filepath.Join(out, name+"."+ext), []byte(content), 0o600); err != nil {
				t.Fatal(err)
			}
		}
		t.Logf("%-20s %s", name, m.Subject)
	}
}
