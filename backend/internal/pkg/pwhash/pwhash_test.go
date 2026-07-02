package pwhash

import "testing"

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
