package twofa

import "testing"

func TestMayRemove_AdminMustKeepAFactor(t *testing.T) {
	t.Parallel()
	if MayRemove(true, false, true, false, true, FactorTOTP) {
		t.Fatal("admin with only TOTP must not remove it")
	}
	if !MayRemove(true, false, true, true, true, FactorTOTP) {
		t.Fatal("admin holding both may remove TOTP")
	}
	if !MayRemove(false, false, true, false, true, FactorTOTP) {
		t.Fatal("policy off leaves admins unconstrained")
	}
}
