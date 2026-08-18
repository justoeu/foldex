package mailer

import "strconv"

// The constructors below name a template and supply its params. They do NOT
// render: rendering happens when the message leaves the outbox, in the
// recipient's locale, which is what lets a copy fix reach a message that was
// queued before it landed.
//
// Every param a catalogue references must be set here, empty string included.
// interpolate runs with `missingkey=error`, so a forgotten key fails the render
// instead of printing `<no value>` into a password-reset e-mail.

// InviteMessage builds the "you were invited" envelope. acceptURL carries the
// raw invite token, which makes the whole URL a credential: it is never logged
// by the smtp driver, and the log driver's decision to print it is the
// documented trade in mailer.go.
func InviteMessage(to, inviterName, acceptURL string, expiresInHours int) Envelope {
	return Envelope{Template: TemplateInvite, To: to, Params: map[string]string{
		ParamBy:           inviterName,
		ParamActionURL:    acceptURL,
		ParamExpiresHours: strconv.Itoa(expiresInHours),
	}}
}

// SessionRevokedMessage warns that a refresh token was replayed and the whole
// session family was killed. It is informational: by the time it is sent the
// sessions are already revoked, so there is no action link — and deliberately
// no link at all, since an unexpected "your session was terminated, click
// here" is the exact shape of a phishing mail.
func SessionRevokedMessage(to string) Envelope {
	return Envelope{Template: TemplateSessionRevoked, To: to}
}

// PasswordResetMessage builds the "reset your password" envelope. Like the
// invite link, resetURL carries a raw token and is therefore a credential.
func PasswordResetMessage(to, resetURL string, expiresInMinutes int) Envelope {
	return Envelope{Template: TemplatePasswordReset, To: to, Params: map[string]string{
		ParamActionURL:      resetURL,
		ParamExpiresMinutes: strconv.Itoa(expiresInMinutes),
	}}
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
func PasswordResetUnavailableMessage(to string) Envelope {
	return Envelope{Template: TemplateResetUnavailable, To: to}
}

// LoginCodeMessage builds the envelope carrying a one-time sign-in code.
//
// The code is in the SUBJECT as well as the body: it is what makes the message
// usable from a notification preview without opening the mail, which is how
// most people actually read it.
func LoginCodeMessage(to, code string, expiresInMinutes int) Envelope {
	return Envelope{Template: TemplateLoginCode, To: to, Params: map[string]string{
		ParamCode:           code,
		ParamExpiresMinutes: strconv.Itoa(expiresInMinutes),
	}}
}

// EnrollEmail2FAMessage builds the envelope that confirms an address AS A
// SECOND FACTOR.
//
// A template of its own rather than a reuse of login_code, whose copy would be
// actively wrong here: it announces a sign-in and warns "if this is not you,
// someone may know your password — change it". Sent to a user who has just
// asked to add e-mail as a factor, that is both untrue and alarming, and would
// push them toward a password change they do not need.
func EnrollEmail2FAMessage(to, code string, expiresInMinutes int) Envelope {
	return Envelope{Template: TemplateEnrollEmail2FA, To: to, Params: map[string]string{
		ParamCode:           code,
		ParamExpiresMinutes: strconv.Itoa(expiresInMinutes),
	}}
}

// StepUpCodeMessage builds the envelope that authorizes a credential change
// from a live session, using the account's enrolled e-mail factor.
//
// Distinct from both neighbours because the reader's situation is distinct. A
// login code means "someone is signing in"; an enrollment code means "someone
// is adding this address". This one means "someone already signed in is about
// to change a security setting" — and that is the message where an unexpected
// arrival matters most, because the change it authorizes may be REMOVING a
// factor.
func StepUpCodeMessage(to, code string, expiresInMinutes int) Envelope {
	return Envelope{Template: TemplateStepUpCode, To: to, Params: map[string]string{
		ParamCode:           code,
		ParamExpiresMinutes: strconv.Itoa(expiresInMinutes),
	}}
}

// VerifyEmailMessage builds the address-confirmation envelope. verifyURL
// carries a raw token and is therefore a credential, like the invite and reset
// links.
func VerifyEmailMessage(to, verifyURL string, expiresInMinutes int) Envelope {
	return Envelope{Template: TemplateVerifyEmail, To: to, Params: map[string]string{
		ParamActionURL:      verifyURL,
		ParamExpiresMinutes: strconv.Itoa(expiresInMinutes),
	}}
}

// AdminPasswordRecoveryMessage carries a user-bound recovery link requested by
// an administrator. It is sent only through SMTP; the log driver must never be
// used for this credential.
func AdminPasswordRecoveryMessage(to, resetURL string, expiresInMinutes int) Envelope {
	return Envelope{Template: TemplateAdminRecovery, To: to, Params: map[string]string{
		ParamActionURL:      resetURL,
		ParamExpiresMinutes: strconv.Itoa(expiresInMinutes),
	}}
}

// AccountConvertedMessage warns that the account now signs in with Google and
// no longer has a password.
//
// It is sent because the conversion RETIRES a credential. Someone who reads
// this and did not do it has had their password used — the conversion required
// it — and the only remedy left is an administrator, because the password reset
// flow no longer applies to an account without a password. Saying that plainly
// is more useful than a generic "your account was updated".
func AccountConvertedMessage(to, googleEmail string) Envelope {
	return Envelope{Template: TemplateAccountConverted, To: to, Params: map[string]string{
		ParamGoogleEmail: googleEmail,
	}}
}

// RecoveryCodeUsedMessage warns that a single-use recovery code was spent.
//
// Recovery codes bypass the authenticator, so spending one is either the user
// replacing a lost phone or an attacker who obtained the sheet. Only the owner
// can tell which, and only if they are told it happened.
func RecoveryCodeUsedMessage(to string, remaining int) Envelope {
	return Envelope{Template: TemplateRecoveryCodeUsed, To: to, Params: map[string]string{
		ParamRemaining: strconv.Itoa(remaining),
	}}
}
