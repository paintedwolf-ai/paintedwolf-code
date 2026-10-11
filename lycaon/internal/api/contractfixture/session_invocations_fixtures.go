package contractfixture

import (
	"context"

	"github.com/lycaon/lycaon/internal/invocation"
	wire "github.com/lycaon/lycaon/pkg/api"
)

type FixedInvocationRecorder struct {
	Items []wire.InvocationReceipt
}

func (r *FixedInvocationRecorder) ListSession(context.Context, string) ([]wire.InvocationReceipt, error) {
	return r.Items, nil
}

func (r *FixedInvocationRecorder) ListSessionPage(ctx context.Context, sessionID string, cursor string, limit int) (wire.InvocationReceiptList, error) {
	return wire.InvocationReceiptList{Invocations: r.Items}, nil
}

func (*FixedInvocationRecorder) Settle(context.Context, string, invocation.Settlement) (*wire.InvocationReceipt, error) {
	return nil, nil
}

func (*FixedInvocationRecorder) Begin(context.Context, invocation.Start) (*wire.InvocationReceipt, error) {
	return nil, nil
}
func (*FixedInvocationRecorder) InterruptRunning(context.Context) (int64, error) { return 0, nil }
