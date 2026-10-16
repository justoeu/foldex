package twofa

import "testing"

func TestMayRemove_AdminMustKeepAFactor(t *testing.T) {
	t.Parallel()
	if MayRemove(RemovalCheck{RequireForAdmins: true, IsAdmin: true, TOTPEnabled: true, Removing: FactorTOTP}) {
		t.Fatal("admin with only TOTP must not remove it")
	}
	if !MayRemove(RemovalCheck{RequireForAdmins: true, IsAdmin: true, EmailEnabled: true, TOTPEnabled: true, Removing: FactorTOTP}) {
		t.Fatal("admin holding both may remove TOTP")
	}
	if !MayRemove(RemovalCheck{IsAdmin: true, TOTPEnabled: true, Removing: FactorTOTP}) {
		t.Fatal("policy off leaves admins unconstrained")
	}
}
