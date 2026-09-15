package folders

import (
	"encoding/json"
	"go/ast"
	"go/parser"
	"go/token"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"foldex/internal/pkg/pwhash"
)

// INV-067: the hint is display text and must never equal the password. Create
// compares plaintext; update with both fields set does the same before the
// repository sees a hash.
func TestS3776_INV067_HintMustNotEqualPassword(t *testing.T) {
	pw := "correct-horse"
	same := "Correct-Horse"
	err := CreateInput{Name: "x", Color: "#abc", Password: &pw, PasswordHint: &same}.Validate()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "must not be the same")

	distinct := "rhymes with force"
	require.NoError(t, CreateInput{Name: "x", Color: "#abc", Password: &pw, PasswordHint: &distinct}.Validate())

	var in UpdateInput
	require.NoError(t, json.Unmarshal([]byte(`{"password":"hunter2","password_hint":"hunter2"}`), &in))
	in.Normalize()
	err = in.Validate()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "must not be the same")
}

// INV-065: protection is two mechanisms. Content-gating is CheckUnlock;
// redaction of RapidView previews is unconditional in List when has_password.
func TestS3776_INV065_ContentGateIsSeparateFromRedaction(t *testing.T) {
	secret := []byte("0123456789abcdef0123456789abcdef")
	hash, err := pwhash.Hash("secret123")
	require.NoError(t, err)
	token := IssueUnlockToken(secret, 1, hash)

	assert.NoError(t, CheckUnlock(secret, 1, nil, ""), "unprotected folder never requires a token")
	assert.NoError(t, CheckUnlock(secret, 1, &hash, token))
	require.ErrorIs(t, CheckUnlock(secret, 1, &hash, ""), ErrLocked)
	require.ErrorIs(t, CheckUnlock(secret, 1, &hash, "garbage"), ErrLocked)

	fn := parseFunc(t, "repository.go", "List")
	require.True(t, listRedactsProtectedPreviews(fn),
		"List must skip unmarshaling previews when HasPassword is true — an unlock token never un-redacts RapidView")
}

// INV-064: a cascade authorizes the root, then refuses if any other locked
// descendant is in the subtree. The count is what the handler returns as 409.
func TestS3776_INV064_CascadeDoesNotCrossUnprovedPasswordBoundary(t *testing.T) {
	wrapped := &descendantProtectedError{Count: 2}
	require.ErrorIs(t, wrapped, ErrDescendantProtected)
	assert.EqualValues(t, 2, wrapped.Count)

	fn := parseFunc(t, "repository.go", "lockCascadeSubtree")
	require.True(t, cascadeCountsProtectedDescendantsExcludingRoot(fn),
		"cascade must count password_hash descendants other than the root — an unlock for the root never authorizes a locked child")
}

func parseFunc(t *testing.T, file, name string) *ast.FuncDecl {
	t.Helper()
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, file, nil, 0)
	require.NoError(t, err)
	var found *ast.FuncDecl
	ast.Inspect(f, func(n ast.Node) bool {
		fn, ok := n.(*ast.FuncDecl)
		if ok && fn.Name.Name == name {
			found = fn
			return false
		}
		return true
	})
	require.NotNil(t, found, "%s must live in %s", name, file)
	return found
}

func listRedactsProtectedPreviews(fn *ast.FuncDecl) bool {
	if fn == nil || fn.Body == nil {
		return false
	}
	found := false
	ast.Inspect(fn.Body, func(n ast.Node) bool {
		ifs, ok := n.(*ast.IfStmt)
		if !ok {
			return true
		}
		un, ok := ifs.Cond.(*ast.UnaryExpr)
		if !ok || un.Op != token.NOT {
			return true
		}
		sel, ok := un.X.(*ast.SelectorExpr)
		if !ok || sel.Sel.Name != "HasPassword" {
			return true
		}
		found = true
		return false
	})
	return found
}

func cascadeCountsProtectedDescendantsExcludingRoot(fn *ast.FuncDecl) bool {
	if fn == nil || fn.Body == nil {
		return false
	}
	found := false
	ast.Inspect(fn.Body, func(n ast.Node) bool {
		ifs, ok := n.(*ast.IfStmt)
		if !ok {
			return true
		}
		bin, ok := ifs.Cond.(*ast.BinaryExpr)
		if !ok || bin.Op != token.LAND {
			return true
		}
		if !binaryComparesIdentToID(bin.X) && !binaryComparesIdentToID(bin.Y) {
			return true
		}
		if !binaryChecksPasswordHash(bin.X) && !binaryChecksPasswordHash(bin.Y) {
			return true
		}
		found = true
		return false
	})
	return found
}

func binaryComparesIdentToID(e ast.Expr) bool {
	bin, ok := e.(*ast.BinaryExpr)
	if !ok || bin.Op != token.NEQ {
		return false
	}
	left, ok := bin.X.(*ast.Ident)
	if !ok || left.Name != "folderID" {
		return false
	}
	right, ok := bin.Y.(*ast.Ident)
	return ok && right.Name == "id"
}

func binaryChecksPasswordHash(e ast.Expr) bool {
	bin, ok := e.(*ast.BinaryExpr)
	if !ok || bin.Op != token.NEQ {
		return false
	}
	left, ok := bin.X.(*ast.Ident)
	if !ok || left.Name != "passwordHash" {
		return false
	}
	_, ok = bin.Y.(*ast.Ident)
	return ok
}
