package toolcontract

// MarkdownNoteContract is the content contract for user-facing text a model
// publishes in place of a call.
const MarkdownNoteContract = "paintedwolf.markdown_note"

// RetiredToPublishedText reports a retired tool whose job is now text the
// model publishes, as opposed to host-owned content it receives.
func RetiredToPublishedText(name string) bool {
	contract, ok := compiledRetiredToContent[name]
	return ok && contract == MarkdownNoteContract
}
