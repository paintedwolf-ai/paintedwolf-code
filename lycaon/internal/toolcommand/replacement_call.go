package toolcommand

// ReplacementCall is one exact tool call a host refusal offers for explicit
// submission; it never executes itself.
type ReplacementCall struct {
	Tool string         `json:"tool"`
	Args map[string]any `json:"args"`
}

// ExternalSelectorLine is the command schema's copy for the explicit external
// selector; the host omits it when the selector is hidden.
const ExternalSelectorLine = "`external -- NAME` runs the installed binary that shares a native name."
