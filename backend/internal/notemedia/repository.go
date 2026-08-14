package notemedia

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"foldex/internal/pkg/authctx"
)

const (
	LeaseTTL       = 24 * time.Hour
	DeleteBatchMax = 100
)

type RestoredRef struct {
	NoteID    int64
	ObjectKey string
}

type ObjectDeleter interface {
	DeleteObject(ctx context.Context, key string) error
}

func RegisterLease(ctx context.Context, pool *pgxpool.Pool, uid authctx.UserID, key string) error {
	_, err := pool.Exec(ctx, `
        INSERT INTO note_media (user_id, object_key, lease_expires_at)
        VALUES ($1, $2, now() + $3::interval)
    `, int64(uid), key, LeaseTTL.String())
	if err != nil {
		return fmt.Errorf("register note media lease: %w", err)
	}
	return nil
}

func ForgetLease(ctx context.Context, pool *pgxpool.Pool, uid authctx.UserID, key string) error {
	_, err := pool.Exec(ctx, `
        DELETE FROM note_media
        WHERE user_id = $1 AND object_key = $2
          AND NOT EXISTS (
              SELECT 1 FROM note_media_ref r
              WHERE r.user_id = note_media.user_id
                AND r.object_key = note_media.object_key
          )
    `, int64(uid), key)
	if err != nil {
		return fmt.Errorf("forget note media lease: %w", err)
	}
	return nil
}

// SyncRefs replaces one note's refs with candidate keys that are already owned
// by uid. Foreign and legacy keys remain renderable in body_html but never gain
// delete/write authority.
func SyncRefs(ctx context.Context, tx pgx.Tx, uid authctx.UserID, noteID int64, keys []string) ([]string, error) {
	rows, err := tx.Query(ctx, `
        DELETE FROM note_media_ref
        WHERE user_id = $1 AND note_id = $2
          AND NOT (object_key = ANY($3::text[]))
        RETURNING object_key
    `, int64(uid), noteID, keys)
	if err != nil {
		return nil, fmt.Errorf("delete stale note media refs: %w", err)
	}
	released, err := scanKeys(rows)
	if err != nil {
		return nil, err
	}

	if len(keys) > 0 {
		if _, err := tx.Exec(ctx, `
            INSERT INTO note_media_ref (user_id, note_id, object_key)
            SELECT $1, $2, m.object_key
            FROM note_media m
            WHERE m.user_id = $1 AND m.object_key = ANY($3::text[])
            ON CONFLICT DO NOTHING
        `, int64(uid), noteID, keys); err != nil {
			return nil, fmt.Errorf("insert note media refs: %w", err)
		}
		if _, err := tx.Exec(ctx, `
            UPDATE note_media m SET lease_expires_at = NULL
            WHERE m.user_id = $1 AND m.object_key = ANY($2::text[])
              AND EXISTS (
                  SELECT 1 FROM note_media_ref r
                  WHERE r.user_id = m.user_id AND r.object_key = m.object_key
              )
        `, int64(uid), keys); err != nil {
			return nil, fmt.Errorf("claim note media refs: %w", err)
		}
	}
	return expireReleased(ctx, tx, uid, released)
}

func ReleaseNoteRefs(ctx context.Context, tx pgx.Tx, uid authctx.UserID, noteID int64) ([]string, error) {
	rows, err := tx.Query(ctx, `
        DELETE FROM note_media_ref
        WHERE user_id = $1 AND note_id = $2
        RETURNING object_key
    `, int64(uid), noteID)
	if err != nil {
		return nil, fmt.Errorf("release note media refs: %w", err)
	}
	released, err := scanKeys(rows)
	if err != nil {
		return nil, err
	}
	return expireReleased(ctx, tx, uid, released)
}

func ReleaseFolderSubtreeRefs(ctx context.Context, tx pgx.Tx, uid authctx.UserID) error {
	rows, err := tx.Query(ctx, `
        DELETE FROM note_media_ref
        WHERE user_id = $1 AND note_id IN (
            SELECT n.id FROM note n
            WHERE n.user_id = $1
              AND n.folder_id IN (SELECT id FROM _cascade_subtree)
        )
        RETURNING object_key
    `, int64(uid))
	if err != nil {
		return fmt.Errorf("release folder note media refs: %w", err)
	}
	released, err := scanKeys(rows)
	if err != nil {
		return err
	}
	_, err = expireReleased(ctx, tx, uid, released)
	return err
}

func ReleaseOwnerRefs(ctx context.Context, tx pgx.Tx, uid authctx.UserID) error {
	rows, err := tx.Query(ctx, `
        DELETE FROM note_media_ref WHERE user_id = $1
        RETURNING object_key
    `, int64(uid))
	if err != nil {
		return fmt.Errorf("release owner note media refs: %w", err)
	}
	released, err := scanKeys(rows)
	if err != nil {
		return err
	}
	_, err = expireReleased(ctx, tx, uid, released)
	return err
}

// RestoreRefs inserts freshly generated ownership rows and refs in batches.
func RestoreRefs(ctx context.Context, tx pgx.Tx, uid authctx.UserID, keys []string, refs []RestoredRef) error {
	if len(keys) > 0 {
		if _, err := tx.Exec(ctx, `
            INSERT INTO note_media (user_id, object_key, lease_expires_at)
            SELECT $1, k, now() + $3::interval
            FROM unnest($2::text[]) AS k
        `, int64(uid), keys, LeaseTTL.String()); err != nil {
			return fmt.Errorf("insert restored note media: %w", err)
		}
	}
	if len(refs) == 0 {
		return nil
	}
	rows := make([][]any, 0, len(refs))
	for _, ref := range refs {
		rows = append(rows, []any{int64(uid), ref.NoteID, ref.ObjectKey})
	}
	if _, err := tx.Exec(ctx, `
        CREATE TEMP TABLE _restore_note_media_ref (
            user_id bigint NOT NULL,
            note_id bigint NOT NULL,
            object_key text NOT NULL
        ) ON COMMIT DROP
    `); err != nil {
		return fmt.Errorf("create restored note media temp table: %w", err)
	}
	if _, err := tx.CopyFrom(ctx, pgx.Identifier{"_restore_note_media_ref"},
		[]string{"user_id", "note_id", "object_key"}, pgx.CopyFromRows(rows)); err != nil {
		return fmt.Errorf("copy restored note media refs: %w", err)
	}
	if _, err := tx.Exec(ctx, `
        INSERT INTO note_media_ref (user_id, note_id, object_key)
        SELECT r.user_id, r.note_id, r.object_key
        FROM _restore_note_media_ref r
        JOIN note_media m
          ON m.user_id = r.user_id AND m.object_key = r.object_key
        ON CONFLICT DO NOTHING
    `); err != nil {
		return fmt.Errorf("insert restored note media refs: %w", err)
	}
	if _, err := tx.Exec(ctx, `
        UPDATE note_media m SET lease_expires_at = NULL
        WHERE m.user_id = $1 AND m.object_key = ANY($2::text[])
          AND EXISTS (
              SELECT 1 FROM note_media_ref r
              WHERE r.user_id = m.user_id AND r.object_key = m.object_key
          )
    `, int64(uid), keys); err != nil {
		return fmt.Errorf("claim restored note media: %w", err)
	}
	return nil
}

// DeleteOwnedUnreferenced holds row locks across the bounded object-store
// deletes, preventing a concurrent note save from claiming a key after the
// authorization check but before DeleteObject.
func DeleteOwnedUnreferenced(ctx context.Context, pool *pgxpool.Pool, uid authctx.UserID, candidates []string, storage ObjectDeleter) (int64, error) {
	if storage == nil || len(candidates) == 0 {
		return 0, nil
	}
	if len(candidates) > DeleteBatchMax {
		candidates = candidates[:DeleteBatchMax]
	}
	tx, err := pool.Begin(ctx)
	if err != nil {
		return 0, fmt.Errorf("begin note media cleanup: %w", err)
	}
	defer tx.Rollback(ctx)
	rows, err := tx.Query(ctx, `
        SELECT m.object_key FROM note_media m
        WHERE m.user_id = $1 AND m.object_key = ANY($2::text[])
          AND m.lease_expires_at IS NOT NULL
          AND NOT EXISTS (
              SELECT 1 FROM note_media_ref r
              WHERE r.user_id = m.user_id AND r.object_key = m.object_key
          )
        ORDER BY m.object_key
        FOR UPDATE OF m
    `, int64(uid), candidates)
	if err != nil {
		return 0, fmt.Errorf("lock note media cleanup: %w", err)
	}
	keys, err := scanKeys(rows)
	if err != nil {
		return 0, err
	}
	deleted := make([]string, 0, len(keys))
	var deleteErr error
	for _, key := range keys {
		if err := storage.DeleteObject(ctx, key); err != nil {
			if deleteErr == nil {
				deleteErr = fmt.Errorf("delete note media object %q: %w", key, err)
			}
			continue
		}
		deleted = append(deleted, key)
	}
	if len(deleted) > 0 {
		if _, err := tx.Exec(ctx, `
            DELETE FROM note_media
            WHERE user_id = $1 AND object_key = ANY($2::text[])
              AND NOT EXISTS (
                  SELECT 1 FROM note_media_ref r
                  WHERE r.user_id = note_media.user_id
                    AND r.object_key = note_media.object_key
              )
        `, int64(uid), deleted); err != nil {
			return 0, fmt.Errorf("delete note media ownership: %w", err)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return 0, fmt.Errorf("commit note media cleanup: %w", err)
	}
	return int64(len(deleted)), deleteErr
}

func expireReleased(ctx context.Context, tx pgx.Tx, uid authctx.UserID, keys []string) ([]string, error) {
	if len(keys) == 0 {
		return nil, nil
	}
	rows, err := tx.Query(ctx, `
        UPDATE note_media m SET lease_expires_at = now()
        WHERE m.user_id = $1 AND m.object_key = ANY($2::text[])
          AND NOT EXISTS (
              SELECT 1 FROM note_media_ref r
              WHERE r.user_id = m.user_id AND r.object_key = m.object_key
          )
        RETURNING m.object_key
    `, int64(uid), keys)
	if err != nil {
		return nil, fmt.Errorf("expire released note media: %w", err)
	}
	return scanKeys(rows)
}

func scanKeys(rows pgx.Rows) ([]string, error) {
	defer rows.Close()
	keys := make([]string, 0)
	for rows.Next() {
		var key string
		if err := rows.Scan(&key); err != nil {
			return nil, err
		}
		keys = append(keys, key)
	}
	return keys, rows.Err()
}
