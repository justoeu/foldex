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
