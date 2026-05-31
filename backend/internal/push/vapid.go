// Package push delivers Web Push notifications via VAPID (RFC 8030).
//
// Single-user threat model (CLAUDE.md §0): no per-user subscription scoping
// — every persisted endpoint is a target. When the model grows multi-user,
// add a user_id column to push_subscription and key Notify() by user.
package push

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"

	webpush "github.com/SherClockHolmes/webpush-go"
)

// VAPIDKeys is the keypair + subject used to sign Web Push requests. Both
// keys are base64url-encoded strings (no padding, the on-wire VAPID format).
type VAPIDKeys struct {
	PublicKey  string
	PrivateKey string
	Subject    string
}

// LoadOrGenerate returns a usable keypair from one of three sources, in
// priority order:
//
//  1. VAPID_PUBLIC_KEY + VAPID_PRIVATE_KEY explicitly set in env. Subject
//     defaults to "mailto:foldex@localhost" when VAPID_SUBJECT is empty.
//  2. Persisted state file at statePath (e.g. /data/vapid.json) — written
//     by a previous boot when env was empty + autoGen=true.
//  3. Generated on the fly (when autoGen=true) and persisted to statePath
//     for subsequent boots. Logs the public key once so the operator can
//     copy it into .env for repeatable deployments.
//
// Returns an error only when env is partial OR autoGen=false AND no state
// file exists. The error message is actionable — it explicitly tells the
// operator which env to set.
func LoadOrGenerate(public, private, subject, statePath string, autoGen bool, logger *slog.Logger) (VAPIDKeys, error) {
	// Path 1: explicit env. Treat partial env as a config bug, not a
	// half-loaded keypair — we'd rather refuse to boot than silently swap
	// the public key the operator pinned.
	if public != "" || private != "" {
		if public == "" || private == "" {
			return VAPIDKeys{}, errors.New(
				"VAPID config incomplete: set both VAPID_PUBLIC_KEY and VAPID_PRIVATE_KEY (or neither)",
			)
		}
		return VAPIDKeys{PublicKey: public, PrivateKey: private, Subject: defaultSubject(subject)}, nil
	}

	// Path 2: persisted state from a previous autogen.
	if statePath != "" {
		if keys, err := readState(statePath); err == nil {
			if subject != "" {
				keys.Subject = subject
			}
			return keys, nil
		}
	}

	// Path 3: generate + persist. Bail when autogen is disabled — the
	// operator opted out, refusing to boot makes the intent explicit.
	if !autoGen {
		return VAPIDKeys{}, errors.New(
			"VAPID keys not configured: set VAPID_PUBLIC_KEY + VAPID_PRIVATE_KEY in env, " +
				"or set VAPID_AUTO_GENERATE=1 to let the server generate and persist them on first boot",
		)
	}
	priv, pub, err := webpush.GenerateVAPIDKeys()
	if err != nil {
		return VAPIDKeys{}, fmt.Errorf("generate vapid keys: %w", err)
	}
	keys := VAPIDKeys{PublicKey: pub, PrivateKey: priv, Subject: defaultSubject(subject)}
	if statePath != "" {
		if err := writeState(statePath, keys); err != nil {
			// Persisting failure is a warn, not fatal — the in-memory keys
			// still work for this boot. Next boot will regenerate (which
			// invalidates existing subscriptions; ugly but recoverable).
			logger.Warn("vapid: persist failed, keys are session-only", "path", statePath, "err", err)
		} else {
			logger.Info("vapid: generated and persisted new keypair", "path", statePath, "public_key", pub)
		}
	} else {
		logger.Warn("vapid: generated session-only keypair (VAPID_STATE_PATH empty)", "public_key", pub)
	}
	return keys, nil
}

func defaultSubject(s string) string {
	if s == "" {
		return "mailto:foldex@localhost"
	}
	return s
}

type stateFile struct {
	PublicKey  string `json:"public_key"`
	PrivateKey string `json:"private_key"`
	Subject    string `json:"subject"`
}

func readState(path string) (VAPIDKeys, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return VAPIDKeys{}, err
	}
	var s stateFile
	if err := json.Unmarshal(data, &s); err != nil {
		return VAPIDKeys{}, err
	}
	if s.PublicKey == "" || s.PrivateKey == "" {
		return VAPIDKeys{}, errors.New("state file missing keys")
	}
	return VAPIDKeys{PublicKey: s.PublicKey, PrivateKey: s.PrivateKey, Subject: defaultSubject(s.Subject)}, nil
}

func writeState(path string, keys VAPIDKeys) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(stateFile{
		PublicKey:  keys.PublicKey,
		PrivateKey: keys.PrivateKey,
		Subject:    keys.Subject,
	}, "", "  ")
	if err != nil {
		return err
	}
	// 0600: VAPID private key is sensitive — anyone with read access to it
	// can send notifications as the server. Avoid umask leaks by being
	// explicit.
	return os.WriteFile(path, data, 0o600)
}
