// Package promptresult names the outcome of running one prompt turn.
package promptresult

// Result identifies the assistant message a prompt turn produced.
type Result struct {
	MessageID string
	// OfferedTools are the tools the final model request could call. They are
	// not persisted: a replayed or recovered result carries none.
	OfferedTools []string `json:"-"`
}

// OfferedToolNames is nil-safe: a missing result offers no tools.
func (r *Result) OfferedToolNames() []string {
	if r == nil {
		return nil
	}
	return r.OfferedTools
}
