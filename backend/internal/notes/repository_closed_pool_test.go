package notes

import (
	"context"
	"errors"
	"log/slog"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"foldex/internal/pkg/authctx"
	"foldex/internal/pkg/crudupdate"
	"foldex/internal/pkg/domainerr"
	"foldex/internal/testsupport"
)

var errNotesForced = errors.New("forced tx failure")

type failNoteTx struct{}

func (failNoteTx) Begin(context.Context) (pgx.Tx, error) { return failNoteTx{}, errNotesForced }
func (failNoteTx) Commit(context.Context) error          { return errNotesForced }
func (failNoteTx) Rollback(context.Context) error        { return nil }
func (failNoteTx) Exec(context.Context, string, ...any) (pgconn.CommandTag, error) {
	return pgconn.CommandTag{}, errNotesForced
}
func (failNoteTx) Query(context.Context, string, ...any) (pgx.Rows, error) {
	return nil, errNotesForced
}
func (failNoteTx) QueryRow(context.Context, string, ...any) pgx.Row { return failNoteRow{} }
func (failNoteTx) CopyFrom(context.Context, pgx.Identifier, []string, pgx.CopyFromSource) (int64, error) {
	return 0, errNotesForced
}
func (failNoteTx) SendBatch(context.Context, *pgx.Batch) pgx.BatchResults { return nil }
func (failNoteTx) LargeObjects() pgx.LargeObjects                         { return pgx.LargeObjects{} }
func (failNoteTx) Prepare(context.Context, string, string) (*pgconn.StatementDescription, error) {
	return nil, errNotesForced
}
func (failNoteTx) Conn() *pgx.Conn { return nil }

type failNoteRow struct{}

func (failNoteRow) Scan(...any) error { return errNotesForced }

type closedPoolUploader struct {
	deleteErr error
	deleted   []string
}

func (u *closedPoolUploader) Upload(context.Context, string, []byte, string) error { return nil }
func (u *closedPoolUploader) GetObject(context.Context, string) ([]byte, string, error) {
	return nil, "", nil
}
func (u *closedPoolUploader) DeleteObject(_ context.Context, key string) error {
	if u.deleteErr != nil {
		return u.deleteErr
	}
	u.deleted = append(u.deleted, key)
	return nil
}

func strPtr(s string) *string { return &s }

func TestRepositoryClosedPool_EveryDBMethodErrors(t *testing.T) {
	t.Parallel()
	repo := NewRepository(testsupport.ClosedPool(t))
	ctx := context.Background()
	uid := authctx.UserID(1)

	require.Error(t, repo.RegisterMediaLease(ctx, uid, "notes/a.jpg"), "RegisterMediaLease")
	require.Error(t, repo.ForgetMediaLease(ctx, uid, "notes/a.jpg"), "ForgetMediaLease")

	_, err := repo.Create(ctx, uid, CreateInput{Title: "Hello", BodyHTML: "<p>hi</p>"})
	require.Error(t, err, "Create derives slug then fails on closed pool")

	_, err = repo.Create(ctx, uid, CreateInput{Title: "Hello", Slug: strPtr("my-note")})
	require.Error(t, err, "Create with explicit slug")
	assert.NotErrorIs(t, err, ErrSlugTaken, "closed pool error must not masquerade as slug collision")

	_, err = repo.Get(ctx, uid, 1)
	require.Error(t, err, "Get")
	assert.NotErrorIs(t, err, domainerr.ErrNotFound, "closed pool error must not masquerade as not-found")

	_, err = repo.List(ctx, uid, ListQuery{Limit: 7})
	require.Error(t, err, "List ordinary sort")

	_, err = repo.List(ctx, uid, ListQuery{Sort: "clicks", Limit: 7})
	require.Error(t, err, "List click-ranked sort")

	_, err = repo.Update(ctx, uid, 1, UpdateInput{Title: strPtr("x")}, nil, "")
	require.Error(t, err, "Update fails at Begin")

	require.Error(t, repo.Delete(ctx, uid, 1, nil, "", nil), "Delete fails at Begin")

	_, err = repo.SystemViewAndResolveByID(ctx, 1, 0)
	require.Error(t, err, "SystemViewAndResolveByID anonymous")

	_, err = repo.SystemViewAndResolveByID(ctx, 1, uid)
	require.Error(t, err, "SystemViewAndResolveByID with viewer")

	_, err = repo.SystemViewAndResolveBySlug(ctx, "some-slug", 0)
	require.Error(t, err, "SystemViewAndResolveBySlug")

	_, err = repo.systemGet(ctx, 1)
	require.Error(t, err, "systemGet")
	assert.NotErrorIs(t, err, domainerr.ErrNotFound)

	_, err = repo.tagsFor(ctx, uid, []int64{1, 2})
	require.Error(t, err, "tagsFor with ids")
}

func TestRepositoryClosedPool_EarlyReturnsByDesign(t *testing.T) {
	t.Parallel()
	repo := NewRepository(testsupport.ClosedPool(t))
	ctx := context.Background()
	uid := authctx.UserID(1)

	got, err := repo.tagsFor(ctx, uid, nil)
	require.NoError(t, err, "tagsFor with no ids returns early without touching the DB")
	assert.Empty(t, got)

	assert.Same(t, repo, repo.WithStorage(&closedPoolUploader{}), "WithStorage is a builder, never errors")
	assert.Same(t, repo, repo.WithLogger(slog.Default()), "WithLogger is a builder, never errors")
	assert.NotNil(t, repo.loggerOrDefault())
}

func TestRepositoryCleanupMedia_ClosedPoolLogsAndReturns(t *testing.T) {
	t.Parallel()
	repo := NewRepository(testsupport.ClosedPool(t))
	ctx := context.Background()
	uid := authctx.UserID(1)

	repo.cleanupMedia(ctx, uid, []string{"notes/a.jpg"}, nil)
	repo.cleanupMedia(ctx, uid, nil, &closedPoolUploader{})
	repo.cleanupMedia(ctx, uid, []string{"notes/a.jpg"}, &closedPoolUploader{})
}

func TestApplyNoteColumns_SlugRegenSurfacesTxError(t *testing.T) {
	t.Parallel()
	b := &crudupdate.SetBuilder{}
	_, err := applyNoteColumns(context.Background(), failNoteTx{}, authctx.UserID(1), 1, b,
		UpdateInput{SlugSet: true})
	require.ErrorIs(t, err, errNotesForced, "slug regen reads the title through the tx")
}

func TestApplyNoteColumns_WithoutSlugNeverTouchesTx(t *testing.T) {
	t.Parallel()
	b := &crudupdate.SetBuilder{}
	pinned := true
	public := true
	var folder int64 = 3
	keys, err := applyNoteColumns(context.Background(), failNoteTx{}, authctx.UserID(1), 1, b, UpdateInput{
		Title:       strPtr("T"),
		BodyHTML:    strPtr(`<img src="/api/files/notes/x.jpg">`),
		Pinned:      &pinned,
		IsPublic:    &public,
		FolderIDSet: true,
		FolderID:    &folder,
	})
	require.NoError(t, err)
	assert.Equal(t, []string{"notes/x.jpg"}, keys)
	assert.False(t, b.Empty())
}

func TestUploaderObjectDeleter_DeletesEachKeyAndSurfacesError(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	up := &closedPoolUploader{}
	require.NoError(t, asObjectDeleter(up).DeleteObjects(ctx, []string{"a", "b"}))
	assert.Equal(t, []string{"a", "b"}, up.deleted)

	failing := &closedPoolUploader{deleteErr: errNotesForced}
	require.ErrorIs(t, asObjectDeleter(failing).DeleteObjects(ctx, []string{"a"}), errNotesForced)
}

func TestIsSlugUniqueViolation_NonPgErrorsAreFalse(t *testing.T) {
	t.Parallel()
	assert.False(t, isSlugUniqueViolation(nil))
	assert.False(t, isSlugUniqueViolation(errors.New("boom")))
}
