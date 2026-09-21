// Package appsetting is the shared Get/Upsert for JSON documents stored in
// app_setting (instance policy, abuse policy). The SQL must stay in lockstep:
// both rows live outside the backup surface (INV-048).
package appsetting

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// GetJSON returns the document for key, or fallback when the row is missing
// or the payload does not parse (nil error). A database error still returns
// fallback so a corrupted settings row cannot take down login; the error is
// for logs.
func GetJSON[T any](ctx context.Context, pool *pgxpool.Pool, key string, fallback T) (T, error) {
	var raw string
	err := pool.QueryRow(ctx, `SELECT value FROM app_setting WHERE key = $1`, key).Scan(&raw)
	if errors.Is(err, pgx.ErrNoRows) {
		return fallback, nil
	}
	if err != nil {
		return fallback, fmt.Errorf("app_setting get %s: %w", key, err)
	}
	return decodeJSONOrFallback([]byte(raw), fallback), nil
}

func decodeJSONOrFallback[T any](raw []byte, fallback T) T {
	out := fallback
	if err := json.Unmarshal(raw, &out); err != nil {
		return fallback
	}
	return out
}

func Upsert(ctx context.Context, pool *pgxpool.Pool, key, value string) error {
	_, err := pool.Exec(ctx, `
		INSERT INTO app_setting (key, value) VALUES ($1, $2)
		ON CONFLICT (key) DO UPDATE SET value = EXCLUDED.value, updated_at = now()`,
		key, value)
	if err != nil {
		return fmt.Errorf("app_setting set %s: %w", key, err)
	}
	return nil
}
