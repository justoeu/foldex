package auth

import "foldex/internal/auth/admin"

func parseAfterID(raw string) int64 { return admin.ParseAfterID(raw) }
