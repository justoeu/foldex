package secrethint

import "testing"

func TestEqualsPassword_TrimsAndFolds(t *testing.T) {
	if !EqualsPassword("secret", "  Secret") {
		t.Fatal("padded password must still match the hint")
	}
	if EqualsPassword("hint", "password") {
		t.Fatal("distinct values must not match")
	}
}
