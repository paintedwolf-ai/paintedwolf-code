package oarcopy

import (
	"slices"

	"github.com/lycaon/lycaon/internal/toolcontract"
)

func syntaxOverrideCommands(tool string) []string {
	if tool != "command" {
		return nil
	}
	catalog, err := toolcontract.ReadOnlyLanguageV1()
	if err != nil {
		return nil
	}
	var names []string
	for _, command := range catalog.Commands {
		for _, form := range command.Forms {
			if slices.Contains(form.Spellings, "--syntax-override-reason") {
				names = append(names, command.Name)
				break
			}
		}
	}
	return names
}
