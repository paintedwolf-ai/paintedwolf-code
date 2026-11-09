package turnload

import "github.com/lycaon/lycaon/internal/toolcontract"

func completeFamilies(standing map[string]Load, loadable map[string]bool, turn string) {
	for _, name := range sortedKeys(standing) {
		selected := standing[name]
		for _, member := range toolcontract.LoadFamilies([]string{name}, func(tool string) bool { return loadable[tool] }) {
			if _, exists := standing[member]; exists {
				continue
			}
			load := Load{Tool: member, Source: SourceCompanion, With: name, Turn: turn}
			if selected.Source == SourceRequested {
				load.Source = SourceRequested
				load.Need = selected.Need
			}
			standing[member] = load
		}
	}
}
