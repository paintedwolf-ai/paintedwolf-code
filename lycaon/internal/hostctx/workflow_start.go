package hostctx

import "context"

type workflowStartAuthKey struct{}

// WithHumanWorkflowStart marks ctx as authorized by a human start path (slash or HTTP).
func WithHumanWorkflowStart(ctx context.Context) context.Context {
	return context.WithValue(ctx, workflowStartAuthKey{}, true)
}

// HumanWorkflowStart reports whether the start was human-authorized.
func HumanWorkflowStart(ctx context.Context) bool {
	v, _ := ctx.Value(workflowStartAuthKey{}).(bool)
	return v
}
