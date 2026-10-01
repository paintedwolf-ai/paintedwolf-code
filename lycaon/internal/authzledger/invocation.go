package authzledger

import "context"

type invocationKey struct{}
type invocation struct{ sessionID, rootSessionID, toolCallID string }

// WithInvocation correlates host observations with the initiating tool call.
func WithInvocation(ctx context.Context, sessionID, rootSessionID, toolCallID string) context.Context {
	return context.WithValue(ctx, invocationKey{}, invocation{sessionID, rootSessionID, toolCallID})
}

func InvocationToolCall(ctx context.Context, sessionID string) string {
	in, ok := ctx.Value(invocationKey{}).(invocation)
	if !ok || sessionID == "" || sessionID != in.sessionID && sessionID != in.rootSessionID {
		return ""
	}
	return in.toolCallID
}
