package guidance

import "errors"

// ErrCloseoutStopped ends repair without authorizing a completion.
var ErrCloseoutStopped = errors.New("closeout cannot complete")
