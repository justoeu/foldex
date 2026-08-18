// Package mailoutbox is the durable hand-off between the transaction that mints
// a credential and the process that mails it.
//
// The rule the package exists to enforce: a credential and the message that
// carries it are written in the SAME transaction, so neither can exist without
// the other. Before it, the handlers reserved a slot in an in-memory dispatcher
// queue before persisting the credential — which bounded the queue but could
// not survive a restart. A deploy between the commit and the send dropped the
// message while the reset token, and its 60-second cooldown, stayed behind: the
// user waited for a link that no longer existed anywhere and could not ask for
// another.
//
// Nothing here renders. The row stores (template, params) and the sink decides
// what to do with it, which is what lets PR2 forward the encrypted params to a
// broker without this package learning anything about brokers.
package mailoutbox

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"

	"foldex/internal/mailer"
	"foldex/internal/pkg/secrets"
)

// ErrNoCipher guards the one construction mistake that would be invisible in
// production: an Outbox wired without a cipher would have to either store the
// params in clear text or drop them, and both are worse than refusing.
var ErrNoCipher = errors.New("mailoutbox: cipher is required")

// Outbox writes messages into the queue. It is the only type auth touches.
type Outbox struct {
	cipher *secrets.Cipher
}

// CipherPurpose is the domain separator for the outbox's encryption subkey.
//
// It is a constant and must stay one: changing the string derives a different
// key and makes every queued row undecryptable — which the relay would settle
// as `undecryptable_payload`, quietly dropping every pending reset link.
const CipherPurpose = "foldex/mail-outbox/payload/v1"

func New(c *secrets.Cipher) (*Outbox, error) {
	if c == nil {
		return nil, ErrNoCipher
	}
	return &Outbox{cipher: c}, nil
}

// NewFromMasterKey derives the outbox's own subkey from AUTH_ENCRYPTION_KEY.
//
// A subkey rather than the master itself: the TOTP seed already encrypts under
// the master, and two domains sharing one AES key share a (key, nonce) space
// whose safety margins then stop being independent. The volumes are not
// comparable either — one seed per user against one ciphertext per reset link,
// sign-in code and invitation.
func NewFromMasterKey(masterKey []byte) (*Outbox, error) {
	c, err := secrets.NewDerivedCipher(masterKey, CipherPurpose)
	if err != nil {
		return nil, fmt.Errorf("mailoutbox: derive cipher: %w", err)
	}
	return New(c)
}

// EnqueueTx writes the message inside the caller's transaction.
//
// It takes the tx rather than a pool ON PURPOSE, and that is the whole design:
// a variant that opened its own connection would commit independently of the
// credential and reintroduce exactly the window this package removes.
//
// The params are encrypted with AES-256-GCM before they touch the row. They
// carry reset links and sign-in codes, and password_reset stores only a sha256
// precisely so that a pg_dump is not an account-takeover kit — writing the live
// link beside it in clear text would hand back what that design refused. The
// authentication tag is what makes the difference from CTR: without it, write
// access to this table is a link-substitution attack, and the victim sees only
// a legitimate-looking recovery e-mail pointing somewhere else.
func (o *Outbox) EnqueueTx(ctx context.Context, tx pgx.Tx, env mailer.Envelope, locale string) error {
	if env.Template == "" || env.To == "" {
		return fmt.Errorf("mailoutbox: envelope needs a template and a recipient")
	}
	params := env.Params
	if params == nil {
		params = map[string]string{}
	}
	plain, err := json.Marshal(params)
	if err != nil {
		return fmt.Errorf("mailoutbox: marshal params: %w", err)
	}
	ciphertext, nonce, err := o.cipher.Encrypt(plain)
	if err != nil {
		return fmt.Errorf("mailoutbox: encrypt params: %w", err)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO mail_outbox (template, recipient, payload_ciphertext, payload_nonce, locale)
		VALUES ($1, $2, $3, $4, $5)`,
		env.Template, env.To, ciphertext, nonce, mailer.NormalizeLocale(locale)); err != nil {
		return fmt.Errorf("mailoutbox: enqueue: %w", err)
	}
	return nil
}

// Open reverses EnqueueTx's encryption for one claimed row.
func (o *Outbox) Open(msg Outgoing) (mailer.Envelope, error) {
	plain, err := o.cipher.Decrypt(msg.Ciphertext, msg.Nonce)
	if err != nil {
		return mailer.Envelope{}, fmt.Errorf("mailoutbox: open payload: %w", err)
	}
	var params map[string]string
	if err := json.Unmarshal(plain, &params); err != nil {
		return mailer.Envelope{}, fmt.Errorf("mailoutbox: unmarshal params: %w", err)
	}
	return mailer.Envelope{Template: msg.Template, To: msg.Recipient, Params: params}, nil
}
