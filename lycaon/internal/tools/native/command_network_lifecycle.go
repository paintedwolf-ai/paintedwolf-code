package native

import (
	"context"
	"sync"

	"github.com/lycaon/lycaon/internal/confine"
	"github.com/lycaon/lycaon/internal/tools"
)

// commandNetworkLifecycle manages process-scoped network attribution.
type commandNetworkLifecycle struct {
	once          sync.Once
	mu            sync.Mutex
	tctx          tools.ToolContext
	toolName      string
	lease         *confine.ActionLease
	directApplied bool
	hosts         []confine.EgressHost
	// leftRunning counts descendants observed at the moment the action ended.
	leftRunning int
}

func newCommandNetworkLifecycle(
	tctx tools.ToolContext,
	toolName string,
	lease *confine.ActionLease,
	directApplied bool,
) *commandNetworkLifecycle {
	return &commandNetworkLifecycle{
		tctx: tctx, toolName: toolName,
		lease: lease, directApplied: directApplied,
	}
}

func (l *commandNetworkLifecycle) complete(ctx context.Context) []confine.EgressHost {
	if l == nil {
		return nil
	}
	l.once.Do(func() {
		var hosts []confine.EgressHost
		var survivors []int
		if l.lease != nil {
			// Read survivors before closing; the lease stops answering after.
			survivors, _ = l.lease.Survivors()
			hosts = l.lease.Close(ctx)
		}
		tools.RecordMediatedEgress(context.WithoutCancel(ctx), l.tctx, l.toolName, hosts)
		if l.directApplied {
			tools.EmitDirectIPLifecycle(l.tctx, tools.DirectIPLifecycleCompleted)
		}
		l.mu.Lock()
		l.hosts = append([]confine.EgressHost(nil), hosts...)
		l.leftRunning = len(survivors)
		l.mu.Unlock()
	})
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]confine.EgressHost(nil), l.hosts...)
}

// leftBehind reports how many descendants outlived the action.
func (l *commandNetworkLifecycle) leftBehind() int {
	if l == nil {
		return 0
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.leftRunning
}
