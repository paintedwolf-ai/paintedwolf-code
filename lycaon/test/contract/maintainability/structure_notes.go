package maintainability

import (
	"strings"

	"github.com/lycaon/lycaon/test/contract/structure"
)

// structureNotes renders the structural diagnostics of every declared
// directory into the budget report. Declared rules are enforced by the
// structure contract tests; these lines are review evidence for the warnings
// above them, naming the relationships worth inspecting.
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