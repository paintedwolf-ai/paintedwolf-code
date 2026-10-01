package extpacks

import (
	"context"
	"time"
)

// gitNetworkTimeout bounds remote Git operations.
const gitNetworkTimeout = 10 * time.Minute

// gitNetworkContext preserves caller cancellation under the network ceiling.
func gitNetworkContext(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(ctx, gitNetworkTimeout)
}
