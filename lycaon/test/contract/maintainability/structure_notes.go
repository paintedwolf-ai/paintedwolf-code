package maintainability

import (
	"strings"

	"github.com/lycaon/lycaon/test/contract/structure"
)

// structureNotes renders structural diagnostics of declared directories into
// the budget report as review evidence for the findings above them.
func structureNotes(root string) []string {
	tree := structure.NewFileSystem(root)
	graph, err := structure.BuildGraph(tree)
	if err != nil {
		return []string{"structure: " + err.Error()}
	}
	loaded, err := structure.LoadDeclarations(tree)
	if err != nil {
		return []string{"structure: " + err.Error()}
	}
	var notes []string
	for _, key := range structure.DeclaredNames(loaded) {
		diagnostics, err := structure.Analyze(graph, key, loaded[key])
		if err != nil {
			notes = append(notes, "structure: "+err.Error())
			continue
		}
		lines := structure.Describe(diagnostics)
		if len(lines) <= 1 {
			// Only the kind line: nothing to weigh for this directory.
			continue
		}
		notes = append(notes, "structure "+key+": "+strings.Join(lines, "; "))
	}
	return notes
}