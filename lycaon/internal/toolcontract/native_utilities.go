package toolcontract

import (
	"sort"
	"strings"
)

// notAdvertised are language syntax words and selectors, not utilities a
// program calls for routine work.
var notAdvertised = map[string]bool{":": true, "[": true, "[[": true, "true": true, "false": true, "command": true, "export": true, "unset": true, "cd": true, "source": true, "type": true, "external": true, "native_path": true}

// NativeUtilitiesLine names the active catalog's native utilities for the
// command schema; subcommand forms collapse into their command.
func NativeUtilitiesLine() (string, error) {
	catalog, err := operationLanguage()
	if err != nil {
		return "", err
	}
	seen := map[string]bool{}
	for _, command := range catalog.Commands {
		name, _, _ := strings.Cut(command.Name, " ")
		if !notAdvertised[name] {
			seen[name] = true
		}
	}
	names := make([]string, 0, len(seen))
	for name := range seen {
		names = append(names, name)
	}
	sort.Strings(names)
	return "Native utilities: " + strings.Join(names, ", ") + ".", nil
}
