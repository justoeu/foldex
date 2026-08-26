package backupagent

import (
	"fmt"
	"io"
	"strings"

	"filippo.io/age"
)

// parseRecipients turns the BACKUP_AGE_RECIPIENTS strings into age recipients.
// X25519 public keys only: the upload path deliberately holds no secret — the
// identity (private) side exists solely for the drill, and never here.
func parseRecipients(raw []string) ([]age.Recipient, error) {
	recipients := make([]age.Recipient, 0, len(raw))
	for i, r := range raw {
		rec, err := age.ParseX25519Recipient(r)
		if err != nil {
			// NEVER echo the value, and never wrap age's error (it embeds the
			// input): the most likely paste mistake here is the PRIVATE
			// identity, and this message flows to slog and on to whatever
			// aggregates container logs. Position only — plus a targeted hint
			// for the one mistake that matters.
			if strings.HasPrefix(strings.TrimSpace(r), "AGE-SECRET-KEY-") {
				return nil, fmt.Errorf("backupagent: BACKUP_AGE_RECIPIENTS entry %d is an age PRIVATE identity — rotate that key now (it may be in your shell history) and configure the age1... public recipient instead", i+1)
			}
			return nil, fmt.Errorf("backupagent: BACKUP_AGE_RECIPIENTS entry %d is not an age X25519 public key (age1...)", i+1)
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
