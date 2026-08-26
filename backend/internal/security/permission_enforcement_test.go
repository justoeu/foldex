package security

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"foldex/internal/pkg/authctx"
)

// TestEveryPermissionIsEnforcedSomewhere fails when the matrix advertises a
// permission no route gates on.
//
// authctx says it in its own doc — "a permission exists here only when some
// route actually gates on it; a matrix with entries nothing enforces is worse
// than no matrix at all, because it reads as a promise the server does not
// keep" — and three of them had drifted out of that rule: backup.export,
// invites.read and invites.write were declared, rendered, and gated nowhere.
//
// While the matrix was compiled that was a documentation gap. ADR-42 turns it
// into a control: an owner unticks invites.write, the PUT commits, the audit
// trail records it, the grid renders it OFF — and that role keeps minting
// accounts. The failure is silent in every direction a person could check.
//
// `roles.assign` is the deliberate exception below: it is the meta-permission
// gating the matrix endpoint itself, and it IS mounted — the scan finds it.
func TestEveryPermissionIsEnforcedSomewhere(t *testing.T) {
	// Constant NAME by permission value, so the scan can look for identifiers
	// rather than string literals: every mount site spells these as
	// authctx.PermX, and a search for "content.write" would miss all of them.
	constName := map[authctx.Permission]string{
		authctx.PermContentRead:        "PermContentRead",
		authctx.PermContentWrite:       "PermContentWrite",
		authctx.PermBackupExport:       "PermBackupExport",
		authctx.PermBackupRestore:      "PermBackupRestore",
		authctx.PermImportRun:          "PermImportRun",
		authctx.PermUsersRead:          "PermUsersRead",
		authctx.PermUsersWrite:         "PermUsersWrite",
		authctx.PermRolesAssign:        "PermRolesAssign",
		authctx.PermInvitesRead:        "PermInvitesRead",
		authctx.PermInvitesWrite:       "PermInvitesWrite",
		authctx.PermAuditRead:          "PermAuditRead",
		authctx.PermPolicyRead:         "PermPolicyRead",
		authctx.PermPolicyWrite:        "PermPolicyWrite",
		authctx.PermInstanceTransfer:   "PermInstanceTransfer",
		authctx.PermInstanceBackupRead: "PermInstanceBackupRead",
	}
	// Locked from BOTH sides: a permission added to the vocabulary without an
	// entry here fails, so the map cannot quietly stop covering the set.
	require.Len(t, constName, len(authctx.AllPermissions),
		"every permission needs an entry here, or the guard stops covering the set")
	for _, p := range authctx.AllPermissions {
		require.Contains(t, constName, p, "permission %q has no constant name mapped", p)
	}

	// Which permission identifiers appear as an argument to a gate.
	gated := map[string]string{}
	root := filepath.Join("..", "..")
	require.NoError(t, filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() || !strings.HasSuffix(path, ".go") ||
			strings.HasSuffix(path, "_test.go") {
			return err
		}
		fset := token.NewFileSet()
		file, perr := parser.ParseFile(fset, path, nil, 0)
		if perr != nil {
			return nil
		}
		ast.Inspect(file, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			// The two gates, wherever they are called from.
			if sel.Sel.Name != "RequirePermission" && sel.Sel.Name != "RequireWrite" {
				return true
			}
			for _, arg := range call.Args {
				argSel, ok := arg.(*ast.SelectorExpr)
				if !ok {
					continue
				}
				if pkg, ok := argSel.X.(*ast.Ident); ok && pkg.Name == "authctx" {
					rel, _ := filepath.Rel(root, path)
					gated[argSel.Sel.Name] = rel
				}
			}
			return true
		})
		return nil
	}))

	require.NotEmpty(t, gated, "fixture precondition: the scan found gate call sites")

	// content.read is the one entry no route gates, and it is not a broken
	// promise: it is LOCKED ON for every role, so it can never be unticked and
	// therefore can never become a toggle that does nothing. Reads are open to
	// any authenticated caller and ownership scoping (ADR-30) is what decides
	// whose rows come back — a gate here could never refuse.
	//
	// The exemption is CHECKED rather than asserted: if that permission is ever
	// unlocked, it becomes toggleable, and this guard starts demanding a gate
	// for it on the same commit.
	exempt := map[authctx.Permission]bool{authctx.PermContentRead: true}
	for p := range exempt {
		require.True(t, authctx.IsPermissionLocked(p),
			"%q is exempt from needing a gate ONLY because it is locked on. It is "+
				"no longer locked, so it is now a toggle an owner can flip with no "+
				"effect — gate it, or re-lock it", p)
		for _, role := range authctx.AllRoles {
			require.True(t, authctx.DefaultGrants()[role][p],
				"%q is exempt only because every role holds it; role %q does not", p, role)
		}
	}

	var unenforced []string
	for _, p := range authctx.AllPermissions {
		if exempt[p] {
			continue
		}
		if _, ok := gated[constName[p]]; !ok {
			unenforced = append(unenforced, string(p))
		}
	}
	assert.Empty(t, unenforced,
		"these permissions are offered by the matrix and gated by no route, so an "+
			"owner toggling them changes nothing while the screen says otherwise — "+
			"mount a gate, or remove the permission from the vocabulary")
}
