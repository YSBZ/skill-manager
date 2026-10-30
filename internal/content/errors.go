package content

import "errors"

var (
	ErrNotFound      = errors.New("content not found")
	ErrWithdrawn     = errors.New("content withdrawn")
	ErrUnavailable   = errors.New("content unavailable")
	ErrCursorExpired = errors.New("content cursor expired")
	ErrBadQuery      = errors.New("bad content query")
)
