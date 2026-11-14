package worker

import "errors"

// isPanicError reports whether a recovered panic value is target.
func isPanicError(recovered any, target error) bool {
	err, ok := recovered.(error)
	return ok && errors.Is(err, target)
}
