package slug

import (
	"context"
	"errors"
	"fmt"
	"math/rand"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"foldex/internal/pkg/authctx"
	"foldex/internal/pkg/domainerr"
)

type titleScannerFunc func(context.Context, string, ...any) pgx.Row

func (f titleScannerFunc) QueryRow(ctx context.Context, sql string, args ...any) pgx.Row {
	return f(ctx, sql, args...)
}

type scanErrorRow struct{ err error }

func (r scanErrorRow) Scan(...any) error { return r.err }

func TestSlugify(t *testing.T) {
	cases := []struct {
		title string
		want  string
	}{
		{"Jira Board — INV-1", "jira-board-inv-1"},
		{"Conexão M2M", "conexao-m2m"},
		{"Hello World", "hello-world"},
		{"  Leading and trailing  ", "leading-and-trailing"},
		{"!!!", ""},
		{"foo@bar.com", "foo-bar-com"},
		{"Café Noir", "cafe-noir"},
		{"über cool", "uber-cool"},
		{"a", "a"},
		{"", ""},
	}
	for _, tc := range cases {
		t.Run(tc.title, func(t *testing.T) {
			got := Slugify(tc.title)
			if got != tc.want {
				t.Fatalf("Slugify(%q) = %q; want %q", tc.title, got, tc.want)
			}
		})
	}
}

func TestSlugifyMaxLen(t *testing.T) {
	// Build a title that exceeds MaxLen and verify truncation to ≤80 chars
	long := "this-is-a-very-long-title-that-definitely-exceeds-the-80-character-slug-limit-and-must-be-truncated-correctly"
	got := Slugify(long)
	want := "this-is-a-very-long-title-that-definitely-exceeds-the-80-character-slug-limit"
	if got != want {
		t.Fatalf("Slugify(long) = %q; want hyphen-boundary %q", got, want)
	}
	if len(got) > MaxLen {
		t.Fatalf("slug %q has len %d, must be ≤ %d", got, len(got), MaxLen)
	}
}

func TestIsValid(t *testing.T) {
	valid := []string{
		"hello-world",
		"a",
		"abc-123",
		"jira-board-inv-1",
	}
	for _, s := range valid {
		t.Run("valid/"+s, func(t *testing.T) {
			if !IsValid(s) {
				t.Fatalf("IsValid(%q) = false; want true", s)
			}
		})
	}

	invalid := []struct {
		s    string
		desc string
	}{
		{"", "empty"},
		{"Hello-World", "uppercase"},
		{"-leading", "leading hyphen"},
		{"trailing-", "trailing hyphen"},
		{"double--hyphen", "double hyphen"},
		{"123", "all digits"},
		{"a_b", "underscore"},
		{"a b", "space"},
	}
	for _, tc := range invalid {
		t.Run("invalid/"+tc.desc, func(t *testing.T) {
			if IsValid(tc.s) {
				t.Fatalf("IsValid(%q) = true; want false", tc.s)
			}
		})
	}
}

func TestUniqueAvailable(t *testing.T) {
	taken := map[string]bool{"foo": true, "foo-2": true}
	got, err := UniqueAvailable(context.Background(), "foo", func(_ context.Context, c string) (bool, error) {
		return taken[c], nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if got != "foo-3" {
		t.Fatalf("got %q want foo-3", got)
	}
}

func TestUniqueAvailableReservesSpaceForSuffix(t *testing.T) {
	base := strings.Repeat("word-", 15) + "abcde"
	seen := make(map[string]struct{})

	got, err := UniqueAvailable(t.Context(), base, func(_ context.Context, candidate string) (bool, error) {
		if len(candidate) > MaxLen {
			t.Fatalf("candidate %q has len %d, must be <= %d", candidate, len(candidate), MaxLen)
		}
		if !IsValid(candidate) {
			t.Fatalf("candidate %q is invalid", candidate)
		}
		if _, duplicate := seen[candidate]; duplicate {
			t.Fatalf("candidate %q was tried more than once", candidate)
		}
		seen[candidate] = struct{}{}
		return len(seen) < 102, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(got, "-102") {
		t.Fatalf("got %q, want suffix -102", got)
	}
}

func TestUniqueAvailablePreservesCompleteBoundarySegment(t *testing.T) {
	base := strings.Repeat("1", 78) + "-a-2"
	got, err := UniqueAvailable(t.Context(), base, func(_ context.Context, candidate string) (bool, error) {
		return false, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if got != strings.Repeat("1", 78)+"-a" {
		t.Fatalf("got %q; complete boundary segment was discarded", got)
	}
	if !IsValid(got) {
		t.Fatalf("candidate %q is invalid", got)
	}
}

func TestUniqueAvailableKeepsTruncatedCandidateNonNumeric(t *testing.T) {
	base := strings.Repeat("1", MaxLen) + "-a"
	got, err := UniqueAvailable(t.Context(), base, func(_ context.Context, candidate string) (bool, error) {
		return false, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) > MaxLen || !IsValid(got) {
		t.Fatalf("candidate %q must remain valid within %d bytes", got, MaxLen)
	}
}

func TestAllocatorReservesSpaceForSuffix(t *testing.T) {
	base := strings.Repeat("word-", 15) + "abcde"
	allocator := NewAllocator(nil)
	seen := make(map[string]struct{})

	for attempt := 1; attempt <= 102; attempt++ {
		candidate, err := allocator.Allocate(base)
		if err != nil {
			t.Fatal(err)
		}
		if len(candidate) > MaxLen {
			t.Fatalf("candidate %q has len %d, must be <= %d", candidate, len(candidate), MaxLen)
		}
		if !IsValid(candidate) {
			t.Fatalf("candidate %q is invalid", candidate)
		}
		if _, duplicate := seen[candidate]; duplicate {
			t.Fatalf("candidate %q was allocated more than once", candidate)
		}
		seen[candidate] = struct{}{}
	}
}

func TestAllocatorReservesAndReleasesInMemory(t *testing.T) {
	a := NewAllocator([]string{"docs", "docs-2"})

	got, err := a.Allocate("docs")
	if err != nil || got != "docs-3" {
		t.Fatalf("Allocate(docs) = %q, %v; want docs-3", got, err)
	}

	a.Release("docs-2")
	got, err = a.Allocate("docs")
	if err != nil || got != "docs-2" {
		t.Fatalf("Allocate(docs) after release = %q, %v; want docs-2", got, err)
	}
}

func TestResolveUpdateMissingRowUsesDomainNotFound(t *testing.T) {
	var gotSQL string
	var gotArgs []any
	scanner := titleScannerFunc(func(_ context.Context, sql string, args ...any) pgx.Row {
		gotSQL = sql
		gotArgs = args
		return scanErrorRow{err: pgx.ErrNoRows}
	})

	_, err := ResolveUpdate(t.Context(), scanner, authctx.UserID(7), "link", 42, nil, nil, "link")
	if !errors.Is(err, domainerr.ErrNotFound) {
		t.Fatalf("ResolveUpdate error = %v; want domainerr.ErrNotFound", err)
	}
	if !strings.Contains(gotSQL, "WHERE user_id = $1 AND id = $2") {
		t.Fatalf("ResolveUpdate query = %q; want owner-first predicates", gotSQL)
	}
	if len(gotArgs) != 2 || gotArgs[0] != int64(7) || gotArgs[1] != int64(42) {
		t.Fatalf("ResolveUpdate args = %#v; want owner then row id", gotArgs)
	}
}

// RACE-HER-005 property: whatever the set of already-taken slugs, the
// allocator either hands out a candidate outside that set (and format-valid),
// or reports exhaustion only when every candidate in its bounded sequence
// was genuinely taken.
func TestCreateWithRetry_NeverEmitsATakenOrInvalidCandidate(t *testing.T) {
	rng := rand.New(rand.NewSource(20260906))
	for iter := 0; iter < 200; iter++ {
		base := randomValidBase(rng)
		taken := map[string]bool{}
		for i := 0; i < rng.Intn(CreateMaxAttempts+1); i++ {
			taken[candidateForAttempt(base, i+1)] = true
		}
		for i := 0; i < rng.Intn(5); i++ {
			taken[randomValidBase(rng)] = true
		}

		var used string
		_, err := CreateWithRetry(context.Background(), fakeBeginner{}, base, false,
			func(err error) bool { return errors.Is(err, errTaken) },
			func(_ context.Context, _ pgx.Tx, candidate string) (int64, error) {
				if taken[candidate] {
					return 0, fmt.Errorf("INSERT ... UNIQUE (%s): %w", candidate, errTaken)
				}
				used = candidate
				return 1, nil
			},
			nil,
		)

		if err != nil {
			if !errors.Is(err, ErrCreateExhausted) {
				t.Fatalf("iter %d: base %q: unexpected error %v", iter, base, err)
			}
			for attempt := 1; attempt <= CreateMaxAttempts; attempt++ {
				if c := candidateForAttempt(base, attempt); !taken[c] {
					t.Fatalf("iter %d: base %q: gave up but candidate %q was free", iter, base, c)
				}
			}
			continue
		}
		if used == "" || taken[used] {
			t.Fatalf("iter %d: base %q: succeeded with taken candidate %q", iter, base, used)
		}
		if !IsValid(used) {
			t.Fatalf("iter %d: base %q: candidate %q violates the slug format", iter, base, used)
		}
	}
}

var errTaken = errors.New("slug taken")

type fakeBeginner struct{}

func (fakeBeginner) Begin(context.Context) (pgx.Tx, error) { return fakeTx{}, nil }

// fakeTx satisfies pgx.Tx with no-ops; CreateWithRetry only round-trips the
// insert callback inside BeginFunc, never touching the tx it is handed.
type fakeTx struct{}

func (fakeTx) Begin(context.Context) (pgx.Tx, error)                      { return nil, errors.New("nested begin") }
func (fakeTx) BeginFunc(ctx context.Context, fn func(pgx.Tx) error) error { return fn(nil) }
func (fakeTx) Commit(context.Context) error                               { return nil }
func (fakeTx) Rollback(context.Context) error                             { return nil }
func (fakeTx) Exec(context.Context, string, ...any) (pgconn.CommandTag, error) {
	return pgconn.NewCommandTag(""), nil
}
func (fakeTx) Query(context.Context, string, ...any) (pgx.Rows, error) {
	return nil, errors.New("not implemented")
}
func (fakeTx) QueryRow(context.Context, string, ...any) pgx.Row { return nil }
func (fakeTx) CopyFrom(context.Context, pgx.Identifier, []string, pgx.CopyFromSource) (int64, error) {
	return 0, errors.New("not implemented")
}
func (fakeTx) SendBatch(context.Context, *pgx.Batch) pgx.BatchResults { return nil }
func (fakeTx) LargeObjects() pgx.LargeObjects                         { return pgx.LargeObjects{} }
func (fakeTx) Prepare(context.Context, string, string) (*pgconn.StatementDescription, error) {
	return nil, errors.New("not implemented")
}
func (fakeTx) Conn() *pgx.Conn { return nil }

// randomValidBase emits a slug-shaped base: [a-z0-9] words joined by single
// hyphens, occasionally long enough to force suffix-reserving truncation.
func randomValidBase(rng *rand.Rand) string {
	words := 1 + rng.Intn(20)
	parts := make([]string, words)
	total := 0
	for i := range parts {
		n := 1 + rng.Intn(8)
		b := make([]byte, n)
		for j := range b {
			if rng.Intn(4) == 0 {
				b[j] = byte('0' + rng.Intn(10))
			} else {
				b[j] = byte('a' + rng.Intn(26))
			}
		}
		parts[i] = string(b)
		total += n + 1
	}
	return strings.Join(parts, "-")
}
