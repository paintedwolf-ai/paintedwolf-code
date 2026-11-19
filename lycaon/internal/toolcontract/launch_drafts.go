package toolcontract

// MaxUnsavedDocuments bounds the unsaved drafts one result names.
const MaxUnsavedDocuments = 16

const (
	UnsavedChangedOutside = "changed_outside"
	UnsavedNotWritten     = "not_written"
	UnsavedAgentEditHeld  = "agent_edit_held"
)

// UnsavedDocument identifies a draft the launch could not publish.
type UnsavedDocument struct {
	Path  string `json:"path"`
	Cause string `json:"cause"`
}

// LaunchDrafts reports drafts left unsaved by the launches in a call.
type LaunchDrafts struct {
	UnsavedDocuments        []UnsavedDocument `json:"unsaved_documents,omitempty"`
	OmittedUnsavedDocuments int               `json:"omitted_unsaved_documents,omitempty"`
}
