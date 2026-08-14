package notes

import "errors"

var (
	ErrSlugTaken  = errors.New("slug already in use")
	ErrStaleWrite = errors.New("note was modified; refetch and retry")
)
