package toolcontract

// MaxUnsavedDocuments bounds the unsaved drafts one result names.
const MaxUnsavedDocuments = 16

// Causes a launch could not save a draft.
const (
	// UnsavedChangedOutside is a draft whose file changed outside its document.
	UnsavedChangedOutside = "changed_outside"
	// UnsavedNotWritten is a draft whose publication failed.
	UnsavedNotWritten = "not_written"
	// UnsavedAgentEditHeld is a draft holding an agent edit that only the
	// reviewed publication door may publish.
	UnsavedAgentEditHeld = "agent_edit_held"
)

// UnsavedDocument is a draft a launch could not save, so its process read the
// file's earlier bytes.
type UnsavedDocument struct {
	Path  string `json:"path"`
	Cause string `json:"cause"`
}

// LaunchDrafts states, on a result, the drafts its launches could not save.
type LaunchDrafts struct {
	UnsavedDocuments        []UnsavedDocument `json:"unsaved_documents,omitempty"`
	OmittedUnsavedDocuments int               `json:"omitted_unsaved_documents,omitempty"`
}
