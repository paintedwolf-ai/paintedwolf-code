package modelcall

import "context"

// ContextPreparation reports evaluated prefix work without a generated response.
type ContextPreparation struct {
	PreparedTokens int
	ReusedTokens   int
}

// ContextPreparer accepts a screened request snapshot and its assembly boundary.
type ContextPreparer interface {
	PrepareContext(context.Context, CompletionRequest) (ContextPreparation, error)
}
