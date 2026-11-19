package command

import (
	"context"
	"sync"

	"github.com/lycaon/lycaon/internal/tools"
)

type programVerificationSource struct {
	mu       sync.Mutex
	identity commandSourceIdentity
	capture  func(context.Context) commandSourceIdentity
	launched bool
	mixed    bool
}

func (v *programVerificationSource) observeLaunch(ctx context.Context, published bool) {
	if v == nil {
		return
	}
	v.mu.Lock()
	defer v.mu.Unlock()
	if published {
		if v.launched {
			// No single source revision covers launches across a publication.
			v.mixed = true
		} else {
			v.identity = v.capture(ctx)
		}
	}
	v.launched = true
}

func (v *programVerificationSource) result() (commandSourceIdentity, bool) {
	if v == nil {
		return commandSourceIdentity{}, false
	}
	v.mu.Lock()
	defer v.mu.Unlock()
	if v.mixed {
		return commandSourceIdentity{}, true
	}
	return v.identity, false
}

func captureVerificationSource(ctx context.Context, tc tools.ToolContext) commandSourceIdentity {
	if !tc.Execution.VerificationCheck {
		return commandSourceIdentity{}
	}
	return readCommandSource(ctx, tc, tools.HostWriteRoot(tc))
}
