package backupagent

import (
	"fmt"
	"io"

	"filippo.io/age"
)

// parseRecipients turns the BACKUP_AGE_RECIPIENTS strings into age recipients.
// X25519 public keys only: the upload path deliberately holds no secret — the
// identity (private) side exists solely for the drill, and never here.
func parseRecipients(raw []string) ([]age.Recipient, error) {
	recipients := make([]age.Recipient, 0, len(raw))
	for _, r := range raw {
		rec, err := age.ParseX25519Recipient(r)
		if err != nil {
			return nil, fmt.Errorf("backupagent: %q is not an age X25519 public key: %w", r, err)
		}
		recipients = append(recipients, rec)
	}
	return recipients, nil
}

// encryptTo wraps dst in an age encryption stream for recipients, or returns
// dst untouched when plaintext was explicitly allowed and no recipients are
// configured (Config.Load already refused the dangerous combination).
//
// age rather than home-grown AES-GCM: the format is chunk-authenticated and
// streamable, and — decisive for disaster recovery — the operator can decrypt
// WITHOUT Foldex (`age -d`). A proprietary envelope turns "lost the host" into
// "lost the backup" (SDD-OPS-BACKUP §8).
func encryptTo(dst io.Writer, recipients []age.Recipient) (io.WriteCloser, error) {
	if len(recipients) == 0 {
		return nopWriteCloser{dst}, nil
	}
	w, err := age.Encrypt(dst, recipients...)
	if err != nil {
		return nil, fmt.Errorf("backupagent: start age stream: %w", err)
	}
	return w, nil
}

type nopWriteCloser struct{ io.Writer }

func (nopWriteCloser) Close() error { return nil }
