package links

import "errors"

var (
	ErrURLTaken   = errors.New("url already in use")
	ErrSlugTaken  = errors.New("slug already in use")
	ErrStaleWrite = errors.New("link was modified; refetch and retry")
)
