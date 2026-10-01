package hostctx

import "context"

type ambientAttachKey struct{}

// WithAmbientAttach marks ctx as an automatic session-create ambient workflow attach.
func WithAmbientAttach(ctx context.Context) context.Context {
	return context.WithValue(ctx, ambientAttachKey{}, true)
}

// AmbientAttach reports whether the workflow start is ambient session attach.
func AmbientAttach(ctx context.Context) bool {
	v, _ := ctx.Value(ambientAttachKey{}).(bool)
	return v
}
