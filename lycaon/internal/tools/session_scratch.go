package tools

import "errors"

// SessionScratchUnavailableCode refuses an @scratch path in an invocation that
// has no session scratch folder.
const SessionScratchUnavailableCode = "SESSION_SCRATCH_UNAVAILABLE"

// ErrSessionScratchUnavailable marks an @scratch path with no folder to resolve against.
var ErrSessionScratchUnavailable = errors.New("session scratch is unavailable")
