package osprocess

import (
	"errors"
)

// ErrExecutableObservationUnavailable means executable identity could not be
// established from a stopped process's kernel image. A pathname is no fallback.
var ErrExecutableObservationUnavailable = errors.New("stopped executable identity observation is unavailable")
