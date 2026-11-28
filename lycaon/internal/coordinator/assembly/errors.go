package assembly

import "errors"

var (
	errPromptEngineNotConfigured = errors.New("prompt engine not configured")
	errTurnReaderNotConfigured   = errors.New("turn reader not configured")
)
