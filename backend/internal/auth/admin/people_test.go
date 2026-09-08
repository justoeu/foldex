package admin

import (
	"testing"

	"foldex/internal/pkg/authctx"
)

func TestAssignableRole_OwnerIsNotAssignable(t *testing.T) {
	t.Parallel()
	if AssignableRole(authctx.RoleOwner) {
		t.Fatal("owner must not be assignable")
	}
	for _, role := range []authctx.Role{authctx.RoleAdmin, authctx.RoleEditor, authctx.RoleViewer} {
		if !AssignableRole(role) {
			t.Fatalf("%s must be assignable", role)
		}
	}
}

func TestValidStatus(t *testing.T) {
	t.Parallel()
	if !ValidStatus("active") || !ValidStatus("disabled") {
		t.Fatal("active and disabled must be valid")
	}
	if ValidStatus("pending") || ValidStatus("") {
		t.Fatal("unknown status must be refused")
	}
}

func TestParseAfterID(t *testing.T) {
	t.Parallel()
	if ParseAfterID("") != 0 || ParseAfterID("-1") != 0 || ParseAfterID("x") != 0 {
		t.Fatal("invalid after id must be 0")
	}
	if ParseAfterID("42") != 42 {
		t.Fatal("got wrong after id")
	}
}
