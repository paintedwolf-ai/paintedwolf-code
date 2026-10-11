package stopping

import "fmt"

func appendStopError(errs []error, operation, sessionID string, err error) []error {
	if err == nil {
		return errs
	}
	return append(errs, fmt.Errorf("%s for session %s: %w", operation, sessionID, err))
}
