package auth

import (
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestOwnActivityCursor(t *testing.T) {
	at := time.Date(2026, 10, 16, 12, 0, 0, 0, time.UTC)
	seed := func() []any { return []any{int64(7), []string{"link.created"}} }

	t.Run("composite cursor binds (created_at, feed id) with no row dereference", func(t *testing.T) {
		frag, args := ownActivityCursor(seed(), -42, &at)
		if frag != " AND (a.created_at, a.id) < ($3, $4)" {
			t.Errorf("frag = %q", frag)
		}
		if want := []any{int64(7), []string{"link.created"}, at, int64(-42)}; !reflect.DeepEqual(args, want) {
			t.Errorf("args = %v, want %v", args, want)
		}
	})

	t.Run("legacy audit id keeps the id-only bound", func(t *testing.T) {
		frag, args := ownActivityCursor(seed(), 9, nil)
		if frag != " AND a.id < $3" {
			t.Errorf("frag = %q", frag)
		}
		if args[2] != int64(9) {
			t.Errorf("args[2] = %v", args[2])
		}
	})

	t.Run("legacy click id resolves through an owner-scoped lookup", func(t *testing.T) {
		frag, args := ownActivityCursor(seed(), -9, nil)
		if !strings.Contains(frag, "SELECT clicked_at FROM click_log WHERE id = $3 AND user_id = $1") {
			t.Errorf("frag = %q", frag)
		}
		if args[2] != int64(9) {
			t.Errorf("args[2] = %v, want negated id 9", args[2])
		}
	})

	t.Run("no cursor leaves args untouched", func(t *testing.T) {
		frag, args := ownActivityCursor(seed(), 0, nil)
		if frag != "" || len(args) != 2 {
			t.Errorf("frag = %q args = %v", frag, args)
		}
	})
}

func TestOwnClicksCursor(t *testing.T) {
	at := time.Date(2026, 10, 16, 12, 0, 0, 0, time.UTC)
	seed := func() []any { return []any{int64(7)} }

	t.Run("composite cursor compares (clicked_at, -id) in feed-id space", func(t *testing.T) {
		frag, args := ownClicksCursor(seed(), 42, &at)
		if frag != " AND (c.clicked_at, -c.id) < ($2, $3)" {
			t.Errorf("frag = %q", frag)
		}
		if want := []any{int64(7), at, int64(42)}; !reflect.DeepEqual(args, want) {
			t.Errorf("args = %v, want %v", args, want)
		}
	})

	t.Run("legacy audit id resolves through an owner-scoped lookup", func(t *testing.T) {
		frag, _ := ownClicksCursor(seed(), 9, nil)
		if !strings.Contains(frag, "SELECT created_at FROM audit_log WHERE id = $2 AND actor_id = $1") {
			t.Errorf("frag = %q", frag)
		}
	})

	t.Run("legacy click id resolves through an owner-scoped lookup", func(t *testing.T) {
		frag, args := ownClicksCursor(seed(), -9, nil)
		if !strings.Contains(frag, "SELECT clicked_at, id FROM click_log WHERE id = $2 AND user_id = $1") {
			t.Errorf("frag = %q", frag)
		}
		if args[1] != int64(9) {
			t.Errorf("args[1] = %v, want negated id 9", args[1])
		}
	})

	t.Run("no cursor leaves args untouched", func(t *testing.T) {
		frag, args := ownClicksCursor(seed(), 0, nil)
		if frag != "" || len(args) != 1 {
			t.Errorf("frag = %q args = %v", frag, args)
		}
	})
}

func TestMergeActivityClicksOrderAndTruncate(t *testing.T) {
	t2 := time.Date(2026, 10, 16, 12, 0, 0, 0, time.UTC)
	t3 := t2.Add(time.Minute)
	out := []AuditEntry{{ID: 1, CreatedAt: t2}}
	clicks := []AuditEntry{{ID: -5, CreatedAt: t2}, {ID: -6, CreatedAt: t3}}

	merged := mergeActivityClicks(out, clicks, 2)
	if len(merged) != 2 {
		t.Fatalf("len = %d, want truncated to 2", len(merged))
	}
	// Newest first; on a timestamp tie the higher feed id wins (audit 1 > click -5).
	if merged[0].ID != -6 || merged[1].ID != 1 {
		t.Errorf("order = [%d %d], want [-6 1]", merged[0].ID, merged[1].ID)
	}
}
