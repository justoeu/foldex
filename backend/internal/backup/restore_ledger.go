package backup

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"sort"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"foldex/internal/pkg/authctx"
)

type restoreLedgerQuerier interface {
	QueryRow(context.Context, string, ...any) pgx.Row
	Query(context.Context, string, ...any) (pgx.Rows, error)
}

type restoreLedger struct {
	inserted Counts
	skipped  Counts
	warnings []string
	files    FileReport
	mapping  idMapping
	complete bool
}

func loadRestoreLedger(ctx context.Context, db restoreLedgerQuerier, uid authctx.UserID, digest [sha256.Size]byte, mode ConflictMode) (restoreLedger, bool, error) {
	ledger := restoreLedger{mapping: newIDMapping()}
	var insertedJSON, skippedJSON, warningsJSON, filesJSON []byte
	err := db.QueryRow(ctx, `
		SELECT inserted, skipped, warnings, file_report,
		       files_completed_at IS NOT NULL
		FROM backup_restore
		WHERE user_id = $1 AND archive_digest = $2 AND mode = $3`,
		int64(uid), digest[:], string(mode)).Scan(
		&insertedJSON, &skippedJSON, &warningsJSON, &filesJSON, &ledger.complete)
	if err != nil {
		if err == pgx.ErrNoRows {
			return ledger, false, nil
		}
		return ledger, false, fmt.Errorf("load restore ledger: %w", err)
	}
	if err := json.Unmarshal(insertedJSON, &ledger.inserted); err != nil {
		return ledger, false, fmt.Errorf("decode restore inserted counts: %w", err)
	}
	if err := json.Unmarshal(skippedJSON, &ledger.skipped); err != nil {
		return ledger, false, fmt.Errorf("decode restore skipped counts: %w", err)
	}
	if err := json.Unmarshal(warningsJSON, &ledger.warnings); err != nil {
		return ledger, false, fmt.Errorf("decode restore warnings: %w", err)
	}
	if len(filesJSON) > 0 {
		if err := json.Unmarshal(filesJSON, &ledger.files); err != nil {
			return ledger, false, fmt.Errorf("decode restore file report: %w", err)
		}
	}
	if ledger.complete {
		return ledger, true, nil
	}

	rows, err := db.Query(ctx, `
		SELECT entity_kind, source_id, target_id
		FROM backup_restore_entity
		WHERE user_id = $1 AND archive_digest = $2 AND mode = $3`,
		int64(uid), digest[:], string(mode))
	if err != nil {
		return ledger, false, fmt.Errorf("load restore entity mappings: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var kind string
		var sourceID, targetID int64
		if err := rows.Scan(&kind, &sourceID, &targetID); err != nil {
			return ledger, false, fmt.Errorf("scan restore entity mapping: %w", err)
		}
		switch kind {
		case "tag":
			ledger.mapping.tagMap[sourceID] = targetID
		case "folder":
			ledger.mapping.folderMap[sourceID] = targetID
		case "link":
			ledger.mapping.linkMap[sourceID] = targetID
		case "note":
			ledger.mapping.noteMap[sourceID] = targetID
		default:
			return ledger, false, fmt.Errorf("load restore entity mapping: unknown kind %q", kind)
		}
	}
	if err := rows.Err(); err != nil {
		return ledger, false, fmt.Errorf("load restore entity mappings: %w", err)
	}

	fileRows, err := db.Query(ctx, `
		SELECT source_key, target_key
		FROM backup_restore_file
		WHERE user_id = $1 AND archive_digest = $2 AND mode = $3`,
		int64(uid), digest[:], string(mode))
	if err != nil {
		return ledger, false, fmt.Errorf("load restore file mappings: %w", err)
	}
	defer fileRows.Close()
	for fileRows.Next() {
		var sourceKey, targetKey string
		if err := fileRows.Scan(&sourceKey, &targetKey); err != nil {
			return ledger, false, fmt.Errorf("scan restore file mapping: %w", err)
		}
		ledger.mapping.noteFiles[sourceKey] = targetKey
	}
	if err := fileRows.Err(); err != nil {
		return ledger, false, fmt.Errorf("load restore file mappings: %w", err)
	}
	return ledger, true, nil
}

type restoreEntityMapping struct {
	kind               string
	sourceID, targetID int64
}

func saveRestoreLedger(ctx context.Context, tx pgx.Tx, uid authctx.UserID, digest [sha256.Size]byte, mode ConflictMode, rep RestoreReport, mapping idMapping) error {
	insertedJSON, err := json.Marshal(rep.Inserted)
	if err != nil {
		return err
	}
	skippedJSON, err := json.Marshal(rep.Skipped)
	if err != nil {
		return err
	}
	warningsJSON, err := json.Marshal(rep.Warnings)
	if err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO backup_restore
		    (user_id, archive_digest, mode, inserted, skipped, warnings)
		VALUES ($1, $2, $3, $4, $5, $6)`,
		int64(uid), digest[:], string(mode), insertedJSON, skippedJSON, warningsJSON); err != nil {
		return fmt.Errorf("insert restore ledger: %w", err)
	}

	entities := make([]restoreEntityMapping, 0,
		len(mapping.tagMap)+len(mapping.folderMap)+len(mapping.linkMap)+len(mapping.noteMap))
	appendKind := func(kind string, values map[int64]int64) {
		for sourceID, targetID := range values {
			entities = append(entities, restoreEntityMapping{kind: kind, sourceID: sourceID, targetID: targetID})
		}
	}
	appendKind("tag", mapping.tagMap)
	appendKind("folder", mapping.folderMap)
	appendKind("link", mapping.linkMap)
	appendKind("note", mapping.noteMap)
	sort.Slice(entities, func(i, j int) bool {
		if entities[i].kind != entities[j].kind {
			return entities[i].kind < entities[j].kind
		}
		return entities[i].sourceID < entities[j].sourceID
	})
	if len(entities) > 0 {
		_, err = tx.CopyFrom(ctx, pgx.Identifier{"backup_restore_entity"},
			[]string{"user_id", "archive_digest", "mode", "entity_kind", "source_id", "target_id"},
			pgx.CopyFromSlice(len(entities), func(i int) ([]any, error) {
				entry := entities[i]
				return []any{int64(uid), digest[:], string(mode), entry.kind, entry.sourceID, entry.targetID}, nil
			}))
		if err != nil {
			return fmt.Errorf("copy restore entity mappings: %w", err)
		}
	}

	keys := make([]string, 0, len(mapping.noteFiles))
	for sourceKey := range mapping.noteFiles {
		keys = append(keys, sourceKey)
	}
	sort.Strings(keys)
	if len(keys) > 0 {
		_, err = tx.CopyFrom(ctx, pgx.Identifier{"backup_restore_file"},
			[]string{"user_id", "archive_digest", "mode", "source_key", "target_key"},
			pgx.CopyFromSlice(len(keys), func(i int) ([]any, error) {
				sourceKey := keys[i]
				return []any{int64(uid), digest[:], string(mode), sourceKey, mapping.noteFiles[sourceKey]}, nil
			}))
		if err != nil {
			return fmt.Errorf("copy restore file mappings: %w", err)
		}
	}
	return nil
}

func completeRestoreLedger(ctx context.Context, pool *pgxpool.Pool, uid authctx.UserID, digest [sha256.Size]byte, mode ConflictMode, files FileReport) error {
	filesJSON, err := json.Marshal(files)
	if err != nil {
		return err
	}
	ct, err := pool.Exec(ctx, `
		UPDATE backup_restore
		SET file_report = $4, files_completed_at = now()
		WHERE user_id = $1 AND archive_digest = $2 AND mode = $3`,
		int64(uid), digest[:], string(mode), filesJSON)
	if err != nil {
		return fmt.Errorf("complete restore ledger: %w", err)
	}
	if ct.RowsAffected() != 1 {
		return fmt.Errorf("complete restore ledger: checkpoint not found")
	}
	return nil
}

func clearRestoreLedgers(ctx context.Context, tx pgx.Tx, uid authctx.UserID) error {
	_, err := tx.Exec(ctx, `DELETE FROM backup_restore WHERE user_id = $1`, int64(uid))
	return err
}
