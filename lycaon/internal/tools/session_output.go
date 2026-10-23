package tools

import "errors"

// SessionOutputUnavailableCode refuses output access without a host session folder.
const SessionOutputUnavailableCode = "SESSION_OUTPUT_UNAVAILABLE"

var ErrSessionOutputUnavailable = errors.New("session output is unavailable")
