package notemedia

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"
)

// SystemSweepExpired deletes at most limit expired, unreferenced objects across
// all tenants. It holds row locks while calling the single-object storage port;
// the cap bounds transaction and object-store work, and SKIP LOCKED allows a
// future second worker without duplicate deletion.
func SystemSweepExpired(ctx context.Context, pool *pgxpool.Pool, storage ObjectDeleter, limit int) (int64, error) {
	if storage == nil {
		return 0, nil
	}
	if limit <= 0 || limit > DeleteBatchMax {
		limit = DeleteBatchMax
	}
	tx, err := pool.Begin(ctx)
	if err != nil {
		return 0, fmt.Errorf("begin note media sweep: %w", err)
	}
	defer tx.Rollback(ctx)
	rows, err := tx.Query(ctx, `
        SELECT m.object_key FROM note_media m
        WHERE m.lease_expires_at <= now()
          AND NOT EXISTS (
              SELECT 1 FROM note_media_ref r
              WHERE r.user_id = m.user_id AND r.object_key = m.object_key
          )
        ORDER BY m.lease_expires_at, m.object_key
        LIMIT $1
        FOR UPDATE OF m SKIP LOCKED
    `, limit)
	if err != nil {
		return 0, fmt.Errorf("select expired note media: %w", err)
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
				deleteErr = fmt.Errorf("delete expired note media object %q: %w", key, err)
			}
			continue
		}
		deleted = append(deleted, key)
	}
	if len(deleted) > 0 {
		if _, err := tx.Exec(ctx, `
            DELETE FROM note_media m
            WHERE m.object_key = ANY($1::text[])
              AND NOT EXISTS (
                  SELECT 1 FROM note_media_ref r
                  WHERE r.user_id = m.user_id AND r.object_key = m.object_key
              )
        `, deleted); err != nil {
			return 0, fmt.Errorf("delete expired note media: %w", err)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return 0, fmt.Errorf("commit note media sweep: %w", err)
	}
	return int64(len(deleted)), deleteErr
}
