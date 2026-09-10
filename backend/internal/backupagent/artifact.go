package backupagent

import (
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"

	"filippo.io/age"

	"foldex/internal/pkg/logsafe"
)

/*
The bridge that lets an administrator download a backup artifact (ADR-48).

This is the one place where the wall INV-171 builds is deliberately crossed,
and the shape of the crossing is the whole safety argument:

  - The credentials never move. This process keeps BACKUP_S3_* and the private
    age identity; the backend gets neither, and cannot list, read or write the
    bucket on its own.
  - The PLAINTEXT never moves either. The artifact is decrypted and immediately
    re-encrypted to a passphrase the caller supplied, in one streaming pass —
    it never lands on disk and never leaves this process in the clear.
  - The caller must hold ArtifactToken. Empty token means the bridge does not
    exist at all, which is the default: an operator who does not set it keeps
    exactly today's posture.

What the crossing DOES concede, stated plainly because a comment that hides it
would be worse than no comment: a compromised backend can ask for any artifact
under a passphrase it chooses, and get it. There is no arrangement that grants
"the web tier can hand a backup to a browser" and withholds that.
*/

// artifactPrefixes are the key namespaces this endpoint will serve. A closed
// set, not a substring check: `key` arrives over HTTP, and the store is an S3
// client that would happily fetch anything else the bucket holds.
var artifactPrefixes = []string{dumpKeyPrefix, userZipKeyPrefix, mirrorKeyPrefix}

// minArtifactPassphrase is a floor, not a policy. The passphrase is the ONLY
// thing standing between the artifact and whoever ends up with the file, and
// age's scrypt work factor does not rescue a four-character secret. The real
// password rules live in the backend's instance policy; this refuses the
// absurd case so a bug there cannot ship a trivially-openable dump.
const minArtifactPassphrase = 12

// ErrArtifactKeyRejected is returned for a key outside artifactPrefixes.
var ErrArtifactKeyRejected = errors.New("backupagent: object key is outside the backup namespaces")

type artifactRequest struct {
	Key        string `json:"key"`
	Passphrase string `json:"passphrase"`
}

type artifactListEntry struct {
	Key   string `json:"key"`
	Size  int64  `json:"size"`
	Mtime string `json:"mtime"`
}

// validArtifactKey reports whether key names an object this endpoint may serve.
//
// The traversal check is not theatre even though S3 keys are not paths: the
// operator-facing prefixes are compared literally, and a key like
// "backups/dump/../../secrets" would satisfy HasPrefix while naming something
// else entirely to any tool that later treats the key as a path.
func validArtifactKey(key string) bool {
	if key == "" || strings.Contains(key, "..") {
		return false
	}
	for _, p := range artifactPrefixes {
		if strings.HasPrefix(key, p) {
			return true
		}
	}
	return false
}

// artifactAuth wraps h in the same bearer check the metrics endpoint uses.
//
// Deliberately a copy of Metrics.Handler's guard rather than a shared helper:
// there are exactly two of them, they protect different things, and the
// metrics comment already explains why it in turn mirrors internal/metrics
// byte for byte. Three call sites would earn an extraction; two subtly
// different copies of a security check age badly, so this one is kept
// identical on purpose.
func artifactAuth(token string, h http.HandlerFunc) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if token == "" {
			http.Error(w, "artifact bridge disabled", http.StatusServiceUnavailable)
			return
		}
		if subtle.ConstantTimeCompare([]byte(r.Header.Get("Authorization")), []byte("Bearer "+token)) != 1 {
			w.Header().Set("WWW-Authenticate", "Bearer")
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		h(w, r)
	})
}

// serveArtifact streams one stored artifact, re-encrypted to the caller's
// passphrase.
//
// POST, not GET: the passphrase travels in the BODY. A query parameter would
// land in this process's access log, in the backend's, and in anything that
// aggregates either — the same reasoning that keeps e-mail credentials in URL
// fragments (INV-036).
func (a *Agent) serveArtifact(w http.ResponseWriter, r *http.Request) {
	var in artifactRequest
	// A dump key plus a passphrase is a few hundred bytes; the cap keeps a
	// malformed or hostile body from being read into memory at all.
	if err := json.NewDecoder(io.LimitReader(r.Body, 8<<10)).Decode(&in); err != nil {
		http.Error(w, "malformed request", http.StatusBadRequest)
		return
	}
	if !validArtifactKey(in.Key) {
		// The key is echoed nowhere: it is caller-controlled and this response
		// reaches a browser through the backend.
		http.Error(w, "object key rejected", http.StatusBadRequest)
		return
	}
	if len([]rune(in.Passphrase)) < minArtifactPassphrase {
		http.Error(w, "passphrase too short", http.StatusBadRequest)
		return
	}

	/* Admission before any work. Refused rather than queued: the caller is the
	   backend, which is holding a browser's connection open, and a request that
	   waits behind two multi-minute streams is a request that times out
	   somewhere less legible than here. */
	select {
	case artifactSlots <- struct{}{}:
		defer func() { <-artifactSlots }()
	default:
		w.Header().Set("Retry-After", "30")
		http.Error(w, "too many artifact downloads in flight", http.StatusTooManyRequests)
		return
	}

	obj, err := a.store.OpenObject(r.Context(), in.Key)
	if err != nil {
		/* Not found and unreachable-bucket are one answer here on purpose: the
		   caller is the backend, which cannot act on the difference, and the
		   agent's own log carries the real reason for the operator.

		   Sanitized because that reason embeds `key`, which arrived over HTTP:
		   an S3 error quotes the object it failed on, so the log line inherits
		   whatever the caller sent. logsafe.String also truncates, which bounds
		   how much attacker-chosen text one failed request can write. */
		a.logger.Warn("artifact open failed", "err", logsafe.String(err.Error()))
		http.Error(w, "artifact unavailable", http.StatusBadGateway)
		return
	}
	defer func() { _ = obj.Close() }()

	plain, err := a.openPlaintext(in.Key, obj)
	if err != nil {
		a.logger.Warn("artifact decrypt failed", "err", logsafe.String(err.Error()))
		http.Error(w, "artifact unavailable", http.StatusBadGateway)
		return
	}

	/* Headers before the first byte, and NO Content-Length: age's output is
	   longer than its input by an amount that depends on the chunking, so any
	   length written here would be a guess the client would then enforce
	   against a stream that does not match it. */
	w.Header().Set("Content-Type", "application/octet-stream")
	w.WriteHeader(http.StatusOK)

	if err := reEncrypt(w, plain, in.Passphrase); err != nil {
		// The status is already written, so the only honest signal left is a
		// truncated body — which age's chunk authentication makes undecryptable
		// rather than silently short. Logging it is what tells the operator.
		a.logger.Error("artifact stream failed", "err", logsafe.String(err.Error()))
	}
}

// openPlaintext unwraps the stored artifact. A plaintext deployment
// (BACKUP_ALLOW_PLAINTEXT, no ".age" suffix) passes through untouched — the
// same rule DrillJob.decrypt applies, and for the same reason.
func (a *Agent) openPlaintext(key string, src io.Reader) (io.Reader, error) {
	if !strings.HasSuffix(key, ".age") {
		return src, nil
	}
	if len(a.identities) == 0 {
		return nil, errors.New("artifact is age-encrypted and " + errNoIdentity)
	}
	return age.Decrypt(src, a.identities...)
}

// reEncrypt copies src into dst through an age scrypt (passphrase) stream.
//
// age rather than an encrypted ZIP: archive/zip cannot encrypt, the Go ZIP-AES
// libraries are unmaintained and ZipCrypto is broken — and age is already this
// project's format, so the operator needs no tool they do not already need to
// open a backup. Decisively, they can open it WITHOUT Foldex (`age -d`).
func reEncrypt(dst io.Writer, src io.Reader, passphrase string) error {
	recipient, err := age.NewScryptRecipient(passphrase)
	if err != nil {
		return fmt.Errorf("scrypt recipient: %w", err)
	}
	out, err := age.Encrypt(dst, recipient)
	if err != nil {
		return fmt.Errorf("start age stream: %w", err)
	}
	if _, err := io.Copy(out, src); err != nil {
		// Close is still attempted: leaving the stream open would leak the
		// buffered chunk, and the copy error is the one worth reporting.
		_ = out.Close()
		return fmt.Errorf("stream artifact: %w", err)
	}
	// The footer is written by Close, so a dropped Close produces a file that
	// age refuses to open — an error that would surface only in a disaster.
	if err := out.Close(); err != nil {
		return fmt.Errorf("finish age stream: %w", err)
	}
	return nil
}

// listArtifacts answers what a prefix holds, so the screen can offer the
// per-user ZIPs — which, unlike a dump, are N objects that no backup_run row
// names.
func (a *Agent) listArtifacts(w http.ResponseWriter, r *http.Request) {
	prefix := r.URL.Query().Get("prefix")
	if !validArtifactKey(prefix) {
		http.Error(w, "prefix rejected", http.StatusBadRequest)
		return
	}
	entries := make([]artifactListEntry, 0, 64)
	err := a.store.WalkObjects(r.Context(), prefix, func(o ObjectInfo) error {
		// A ceiling, because the answer is built in memory and the bucket's
		// size is not this process's to assume. Truncation is visible to the
		// operator as "the newest N", never as a short list pretending to be
		// complete — the screen states the cap.
		if len(entries) >= maxArtifactListing {
			return errStopWalk
		}
		entries = append(entries, artifactListEntry{
			Key: o.Key, Size: o.Size, Mtime: o.LastModified.UTC().Format("2006-01-02T15:04:05Z"),
		})
		return nil
	})
	if err != nil && !errors.Is(err, errStopWalk) {
		a.logger.Warn("artifact listing failed", "err", logsafe.String(err.Error()))
		http.Error(w, "listing unavailable", http.StatusBadGateway)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"objects":  entries,
		"capped":   len(entries) >= maxArtifactListing,
		"max_keys": maxArtifactListing,
	})
}

// maxArtifactListing caps one listing response. Generous for a per-user ZIP
// namespace on a self-hosted instance, and bounded regardless.
const maxArtifactListing = 500

/*
maxConcurrentArtifacts bounds how many re-encryptions run at once, and the
number comes from arithmetic rather than taste.

age's scrypt defaults are N=2^18, r=8, p=1, so ONE call holds 128·N·r ≈ 256 MiB
for the ~1 s it runs. The agent's container is capped at 1 GiB
(docker-compose.yml) and also has to fit the Go runtime, the streaming buffers,
and — at 03:30 — a pg_dump of the whole database being compressed and
encrypted. Four simultaneous downloads is a gigabyte of scrypt working set
alone: Docker OOM-kills the container, and it takes the running backup with it.

That would be a side channel straight around INV-172's exclusivity: the
advisory lock stops two dumps from running together and says nothing about a
download killing the process mid-dump.

Two, not one: an operator retrying a stalled download should not have to wait
out the first. The work factor stays at age's default — lowering it would make
every downloaded artifact easier to crack in exchange for a limit that a
semaphore already provides for free.
*/
const maxConcurrentArtifacts = 2

// artifactSlots is the semaphore. Package-level because the routes are mounted
// once per process and the budget is the CONTAINER's memory, not any one
// Agent's.
var artifactSlots = make(chan struct{}, maxConcurrentArtifacts)
