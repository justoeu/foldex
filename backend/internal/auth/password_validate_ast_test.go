package auth

import (
	"context"
	"go/ast"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"foldex/internal/pkg/httperr"
)

// TestPasswordValidationHasOneOwner is the DUP-ECH-004 twin of DUP-ECH-002:
// two methods independently rune-count a password against max(policy, 8).
// A third bound added to one writer of credentials would miss admin-create
// (or vice versa). The scan looks at the floor BODY (rune-count + byte cap)
// as well as the envelope codes, because renaming the helper — or swapping
// the envelope string — is how a second copy would hide.
func TestPasswordValidationHasOneOwner(t *testing.T) {
	var owners []string
	err := walkProductionFuncs(func(path string, fn *ast.FuncDecl) {
		if !ownsPasswordFloor(fn) {
			return
		}
		owners = append(owners, path+":"+funcName(fn))
	})
	require.NoError(t, err)
	require.Len(t, owners, 1,
		"password floor (rune-count / byte-cap / envelope) must have one owner, found %v", owners)
}

func ownsPasswordFloor(fn *ast.FuncDecl) bool {
	if fn.Body == nil {
		return false
	}
	var hasRuneCount, hasBound, hasEnvelope bool
	ast.Inspect(fn.Body, func(n ast.Node) bool {
		switch x := n.(type) {
		case *ast.SelectorExpr:
			if x.Sel.Name == "RuneCountInString" {
				if id, ok := x.X.(*ast.Ident); ok && id.Name == "utf8" {
					hasRuneCount = true
				}
			}
		case *ast.Ident:
			switch x.Name {
			case "MaxPasswordLen", "MinPasswordLen", "passwordFloorOf":
				hasBound = true
			}
		case *ast.BasicLit:
			if strings.Contains(x.Value, "password_too_short") ||
				strings.Contains(x.Value, "password_too_long") {
				hasEnvelope = true
			}
		}
		return true
	})
	return hasEnvelope || (hasRuneCount && hasBound)
}

func TestValidatePassword_BothHandlersRefuseTooShortAndTooLong(t *testing.T) {
	ctx := context.Background()
	handlers := []struct {
		name string
		fn   func(context.Context, string) error
	}{
		{"Handler", (&Handler{}).validatePassword},
		{"AdminHandler", (&AdminHandler{}).validatePassword},
	}
	for _, h := range handlers {
		t.Run(h.name+"/short", func(t *testing.T) {
			var he *httperr.Error
			require.ErrorAs(t, h.fn(ctx, "short"), &he)
			assert.Equal(t, "password_too_short", he.Code)
		})
		t.Run(h.name+"/long", func(t *testing.T) {
			var he *httperr.Error
			require.ErrorAs(t, h.fn(ctx, strings.Repeat("x", MaxPasswordLen+1)), &he)
			assert.Equal(t, "password_too_long", he.Code)
		})
	}
}
