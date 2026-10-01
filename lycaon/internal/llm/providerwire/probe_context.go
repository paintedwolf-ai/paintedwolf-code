package providerwire

import (
	"context"
	"time"
)

// ProbeContext derives a context for a sidecar probe an adapter makes beside
// a model request, such as a runner's residency or context-length read. The
// probe follows the request's cancellation and its own timeout but carries
// none of the request's values, so the request's transport trace does not
// count the probe's connection as a request attempt.
func ProbeContext(parent context.Context, timeout time.Duration) (context.Context, context.CancelFunc) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	stop := context.AfterFunc(parent, cancel)
	return ctx, func() {
		stop()
		cancel()
	}
}
