package syntaxhealth

import "context"

type overrideKey struct{}

// WithOverride scopes an explicit parser override to one mutation invocation.
func WithOverride(ctx context.Context, reason string) context.Context {
	return context.WithValue(ctx, overrideKey{}, reason)
}

func OverrideReason(ctx context.Context) string {
	reason, _ := ctx.Value(overrideKey{}).(string)
	return reason
}

func overridden(ctx context.Context) bool {
	return ctx.Err() == nil && OverrideReason(ctx) != ""
}
