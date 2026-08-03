package folders

import "fmt"

// SQLNotInLockedFolder returns a SQL predicate (no leading AND/WHERE) that is
// true when the row is not inside a password-protected folder.
//
// entityAlias is the table alias that has folder_id (e.g. "l", "n").
// Used by list/export/stats/public-resolve paths so locked-folder content is
// never leaked without a successful unlock on the scoped folder_id routes.
func SQLNotInLockedFolder(entityAlias string) string {
	return fmt.Sprintf(`(%[1]s.folder_id IS NULL OR NOT EXISTS (
            SELECT 1 FROM folder _lf
            WHERE _lf.id = %[1]s.folder_id AND _lf.password_hash IS NOT NULL
        ))`, entityAlias)
}
