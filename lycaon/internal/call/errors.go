package call

import "errors"

var (
	// ErrPathEscape is returned when a reservation path escapes the project root.
	ErrPathEscape = errors.New("HANDOFF_PATH_ESCAPE")
	// ErrSessionNotFound is returned when the handoff session does not exist.
	ErrSessionNotFound = errors.New("HANDOFF_SESSION_NOT_FOUND")
)
