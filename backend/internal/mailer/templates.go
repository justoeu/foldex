package mailer

import (
	"fmt"
	"html"
	"strings"
)

// The templates are plain string building rather than html/template because
// every dynamic value here is either a URL this server just minted or an
// e-mail address it just validated — there is no rich user content to render.
// What DOES need care is the HTML arm, where an address containing `<` would
// otherwise break out of its element, so those values go through html.Escape.

// InviteMessage builds the "you were invited" e-mail. acceptURL already carries
// the raw invite token, which makes the whole URL a credential: it is never
// logged by the smtp driver, and the log driver's decision to print it is the
// documented trade in mailer.go.
func InviteMessage(to, inviterName, acceptURL string, expiresInHours int) Message {
	by := ""
	if inviterName != "" {
		by = fmt.Sprintf(" by %s", inviterName)
	}
	text := strings.Join([]string{
		"You have been invited" + by + " to Foldex.",
		"",
		"Open the link below to choose a password and activate your account:",
		acceptURL,
		"",
		fmt.Sprintf("The link expires in %d hours and can be used once.", expiresInHours),
		"If you were not expecting this invitation, you can ignore this message.",
	}, "\n")

	htmlBody := fmt.Sprintf(
		`<p>You have been invited%s to <strong>Foldex</strong>.</p>`+
			`<p><a href="%s">Choose a password and activate your account</a></p>`+
			`<p style="color:#666;font-size:13px">The link expires in %d hours and can be used once. `+
			`If you were not expecting this invitation, you can ignore this message.</p>`,
		html.EscapeString(by), html.EscapeString(acceptURL), expiresInHours,
	)

	return Message{To: to, Subject: "You have been invited to Foldex", Text: text, HTML: htmlBody}
}

// SessionRevokedMessage warns that a refresh token was replayed and the whole
// session family was killed. It is informational: by the time it is sent the
// sessions are already revoked, so there is no action link — and deliberately
// no link at all, since an unexpected "your session was terminated, click
// here" is the exact shape of a phishing mail.
func SessionRevokedMessage(to string) Message {
	text := strings.Join([]string{
		"A session token for your Foldex account was replayed, which usually means",
		"it was copied from your device.",
		"",
		"As a precaution every active session was signed out. Sign in again to continue.",
		"If this was not you, change your password after signing in.",
	}, "\n")
	return Message{
		To:      to,
		Subject: "Your Foldex sessions were signed out",
		Text:    text,
	}
}

// PasswordResetMessage builds the "reset your password" e-mail. Like the invite
// link, resetURL carries a raw token and is therefore a credential.
func PasswordResetMessage(to, resetURL string, expiresInMinutes int) Message {
	text := strings.Join([]string{
		"Someone asked to reset the password for your Foldex account.",
		"",
		"Open the link below to choose a new password:",
		resetURL,
		"",
		fmt.Sprintf("The link expires in %d minutes and can be used once.", expiresInMinutes),
		"If you did not ask for this, you can ignore this message — your password will not change.",
	}, "\n")

	htmlBody := fmt.Sprintf(
		`<p>Someone asked to reset the password for your <strong>Foldex</strong> account.</p>`+
			`<p><a href="%s">Choose a new password</a></p>`+
			`<p style="color:#666;font-size:13px">The link expires in %d minutes and can be used once. `+
			`If you did not ask for this, you can ignore this message — your password will not change.</p>`,
		html.EscapeString(resetURL), expiresInMinutes,
	)

	return Message{To: to, Subject: "Reset your Foldex password", Text: text, HTML: htmlBody}
}

// PasswordResetUnavailableMessage answers a reset request for an account that
// has no password to reset — one that signs in with Google (ADR-31).
//
// It exists because /password/forgot always answers 202: staying silent for
// these accounts would make the ENDPOINT indistinguishable, but leave the
// INBOX as the oracle, since only registered addresses ever receive anything.
// Sending a different message keeps both sides uniform. It deliberately carries
// no link — a reset link here would let the mailbox alone resurrect a password
// credential, which is exactly what requiring the current password during
// conversion refused to allow.
func PasswordResetUnavailableMessage(to string) Message {
	text := strings.Join([]string{
		"Someone asked to reset the password for your Foldex account.",
		"",
		"This account signs in with Google, so it has no password to reset.",
		"Use \"Continue with Google\" on the sign-in screen.",
		"",
		"If you did not ask for this, you can ignore this message.",
	}, "\n")

	htmlBody := `<p>Someone asked to reset the password for your <strong>Foldex</strong> account.</p>` +
		`<p>This account signs in with Google, so it has no password to reset. ` +
		`Use &ldquo;Continue with Google&rdquo; on the sign-in screen.</p>` +
		`<p style="color:#666;font-size:13px">If you did not ask for this, you can ignore this message.</p>`

	return Message{To: to, Subject: "Reset your Foldex password", Text: text, HTML: htmlBody}
}

// LoginCodeMessage builds the e-mail carrying a one-time sign-in code.
//
// The code is in the SUBJECT as well as the body: it is what makes the message
// usable from a notification preview without opening the mail, which is how
// most people actually read it.
func LoginCodeMessage(to, code string, expiresInMinutes int) Message {
	text := strings.Join([]string{
		"Your Foldex sign-in code is: " + code,
		"",
		fmt.Sprintf("It expires in %d minutes and can be used once.", expiresInMinutes),
		"If you are not signing in right now, someone may know your password — change it.",
	}, "\n")

	htmlBody := fmt.Sprintf(
		`<p>Your <strong>Foldex</strong> sign-in code is:</p>`+
			`<p style="font-size:28px;letter-spacing:6px;font-weight:700">%s</p>`+
			`<p style="color:#666;font-size:13px">It expires in %d minutes and can be used once. `+
			`If you are not signing in right now, someone may know your password — change it.</p>`,
		html.EscapeString(code), expiresInMinutes,
	)

	return Message{To: to, Subject: "Foldex sign-in code: " + code, Text: text, HTML: htmlBody}
}

// VerifyEmailMessage builds the address-confirmation e-mail. verifyURL carries
// a raw token and is therefore a credential, like the invite and reset links.
func VerifyEmailMessage(to, verifyURL string, expiresInMinutes int) Message {
	text := strings.Join([]string{
		"Confirm this address for your Foldex account by opening the link below:",
		verifyURL,
		"",
		fmt.Sprintf("The link expires in %d minutes and can be used once.", expiresInMinutes),
		"If you did not ask for this, you can ignore this message.",
	}, "\n")

	htmlBody := fmt.Sprintf(
		`<p>Confirm this address for your <strong>Foldex</strong> account:</p>`+
			`<p><a href="%s">Confirm my e-mail address</a></p>`+
			`<p style="color:#666;font-size:13px">The link expires in %d minutes and can be `+
			`used once. If you did not ask for this, you can ignore this message.</p>`,
		html.EscapeString(verifyURL), expiresInMinutes,
	)

	return Message{To: to, Subject: "Confirm your Foldex e-mail", Text: text, HTML: htmlBody}
}

// RecoveryCodeUsedMessage warns that a single-use recovery code was spent.
//
// Recovery codes bypass the authenticator, so spending one is either the user
// replacing a lost phone or an attacker who obtained the sheet. Only the owner
// can tell which, and only if they are told it happened.
func RecoveryCodeUsedMessage(to string, remaining int) Message {
	text := strings.Join([]string{
		"A recovery code was just used to sign in to your Foldex account.",
		"",
		fmt.Sprintf("You have %d recovery codes left.", remaining),
		"If this was not you, change your password and regenerate your recovery codes now.",
	}, "\n")

	htmlBody := fmt.Sprintf(
		`<p>A recovery code was just used to sign in to your <strong>Foldex</strong> account.</p>`+
			`<p>You have <strong>%d</strong> recovery codes left.</p>`+
			`<p style="color:#666;font-size:13px">If this was not you, change your password and `+
			`regenerate your recovery codes now.</p>`,
		remaining,
	)

	return Message{To: to, Subject: "A Foldex recovery code was used", Text: text, HTML: htmlBody}
}
