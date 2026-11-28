package toolcontract

import (
	"fmt"
	"strings"
)

// MarkdownNoteContract is the content contract for user-facing text a model
// publishes in place of a call.
const MarkdownNoteContract = "paintedwolf.markdown_note"

// Retirement is the successor schemas/agent-public-retirements.json records for
// a retired tool: tool names, or a content contract when no call replaces it.
type Retirement struct {
	Successors      []string
	ContentContract string
}

// Retired reports the recorded successor of a retired tool name.
func Retired(name string) (Retirement, bool) {
	retirement, ok := compiledRetirements[strings.TrimSpace(name)]
	return retirement, ok
}

// RetiredToolError is an authored reference to a retired tool. Site names where
// the reference was authored; Retirement carries the recorded successor.
type RetiredToolError struct {
	Site       string
	Tool       string
	Retirement Retirement
}

func (e *RetiredToolError) Error() string {
	return fmt.Sprintf("%s names retired tool %q", e.Site, e.Tool)
}

// RetiredUse refuses an authored name that the host retired. A name still live
// as a tool or command operation is not a retired use.
func RetiredUse(site, name string) error {
	name = strings.TrimSpace(name)
	if _, live := Lookup(name); live || IsCommandOperation(name) {
		return nil
	}
	retirement, ok := Retired(name)
	if !ok {
		return nil
	}
	return &RetiredToolError{Site: site, Tool: name, Retirement: retirement}
}

// RetiredToPublishedText reports a retired tool whose job is now text the
// model publishes, as opposed to host-owned content it receives.
func RetiredToPublishedText(name string) bool {
	contract, ok := compiledRetiredToContent[name]
	return ok && contract == MarkdownNoteContract
}
