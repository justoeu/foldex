package preview

import (
	"context"
	"testing"
)

// IsPublicURL is a thin wrapper around the SSRF helpers — these checks lock in
// the contract used by the screenshot fallback gate (never burn Chrome on a
// host that can't reasonably exist on the public internet).

func TestIsPublicURL_RejectsInvalidScheme(t *testing.T) {
	if IsPublicURL(context.Background(), "file:///etc/passwd") {
		t.Fatalf("file:// must not be considered public")
	}
	if IsPublicURL(context.Background(), "javascript:alert(1)") {
		t.Fatalf("javascript: must not be considered public")
	}
}

func TestIsPublicURL_RejectsMalformed(t *testing.T) {
	if IsPublicURL(context.Background(), "::::not a url") {
		t.Fatalf("garbage input must not be considered public")
	}
}

func TestIsPublicURL_RejectsLoopback(t *testing.T) {
	// `localhost` resolves to 127.0.0.1 — explicitly private under our gate.
	if IsPublicURL(context.Background(), "http://localhost:8080/x") {
		t.Fatalf("localhost must be rejected")
	}
	if IsPublicURL(context.Background(), "http://127.0.0.1/") {
		t.Fatalf("127.0.0.1 must be rejected")
	}
}

func TestIsPublicURL_RejectsEmptyHost(t *testing.T) {
	if IsPublicURL(context.Background(), "http:///just-a-path") {
		t.Fatalf("URL without host must be rejected")
	}
}

func TestIsPublicURL_RejectsUnresolvableHost(t *testing.T) {
	// A guaranteed-NXDOMAIN host (see RFC 6761 §6.4). LookupIP returns an
	// error, which is the same outcome as no IPs — fallback must skip.
	if IsPublicURL(context.Background(), "http://nx.invalid./") {
		t.Fatalf("invalid TLD must be rejected (lookup fails)")
	}
}

func TestIsPublicURL_RejectsRFC1918(t *testing.T) {
	for _, u := range []string{
		"http://10.0.0.5/",
		"http://192.168.1.10/",
		"http://172.20.0.7/",
	} {
		if IsPublicURL(context.Background(), u) {
			t.Fatalf("%s must be rejected as private", u)
		}
	}
}
