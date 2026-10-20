package captureprojection

import "context"

// Value screens a decoded JSON-value tree before any potentially lossy text
// encoding. Raw string and key bytes reach the existing matcher unchanged.
// Callers admit and retain the input, recursive projection and detector working
// storage through consumption. This borrows that reservation; it creates none.
// Structs and arbitrary Go objects refuse rather than bypass string screening.
func (p *Projector) Value(ctx context.Context, scope Scope, label string, value any) (any, Metadata, error) {
	if p == nil || p.matcher == nil {
		return nil, Metadata{}, ErrUnavailable
	}
	metadata := Metadata{StructuredCoverage: true}
	projected, err := p.projectJSONValue(ctx, scope, label, value, &metadata)
	if err != nil {
		return nil, Metadata{}, err
	}
	return projected, metadata, nil
}
