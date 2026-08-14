package pwhash

import (
	"testing"
	"time"
)

func TestHashAndVerify(t *testing.T) {
	hash, err := Hash("correct-horse")
	if err != nil {
		t.Fatalf("Hash: %v", err)
	}
	if hash == "correct-horse" {
		t.Fatal("hash must not equal the plaintext")
	}
	if !Verify(hash, "correct-horse") {
		t.Fatal("Verify should accept the correct password")
	}
	if Verify(hash, "wrong") {
		t.Fatal("Verify should reject a wrong password")
	}
	if Verify("not-a-bcrypt-hash", "correct-horse") {
		t.Fatal("Verify should reject a malformed hash")
	}
}

func TestHashIsSalted(t *testing.T) {
	a, _ := Hash("same")
	b, _ := Hash("same")
	if a == b {
		t.Fatal("two hashes of the same password must differ (random salt)")
	}
}

func TestIsSupportedRejectsMalformedAndWrongCost(t *testing.T) {
	hash, err := Hash("correct-horse")
	if err != nil {
		t.Fatalf("Hash: %v", err)
	}
	if !IsSupported(hash) {
		t.Fatal("Hash output must be supported")
	}

	extremeCost := hash[:4] + "31" + hash[6:]
	if IsSupported(extremeCost) {
		t.Fatal("cost 31 must be rejected before comparison")
	}
	if IsSupported(hash[:len(hash)-1] + "!") {
		t.Fatal("malformed bcrypt alphabet must be rejected")
	}
}

func TestVerifyRejectsExtremeCostBeforeBcrypt(t *testing.T) {
	hash, err := Hash("correct-horse")
	if err != nil {
		t.Fatalf("Hash: %v", err)
	}
	// Change only the declared cost. Generating a real cost-31 hash would make
	// the regression test itself consume an impractical amount of CPU.
	extremeCost := hash[:4] + "31" + hash[6:]

	start := time.Now()
	if Verify(extremeCost, "correct-horse") {
		t.Fatal("Verify must reject an unsupported cost")
	}
	if elapsed := time.Since(start); elapsed > 100*time.Millisecond {
		t.Fatalf("Verify did not fail fast: %v", elapsed)
	}
}
