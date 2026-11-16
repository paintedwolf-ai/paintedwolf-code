package contribution

import (
	"fmt"
	"sort"
)

// HandlerInventories carries native UI handler coverage.
type HandlerInventories struct {
	Den []string
	// NonCommandCallbacks excludes callbacks that are not commands.
	NonCommandCallbacks []string
}

// ValidateHandlerCoverage checks exact declaration and handler coverage.
func ValidateHandlerCoverage(set *Set, inventories HandlerInventories) []error {
	den := map[string]bool{}
	for _, id := range inventories.Den {
		den[id] = true
	}
	nonCommand := map[string]bool{}
	for _, id := range inventories.NonCommandCallbacks {
		nonCommand[id] = true
	}

	var problems []string
	declaredBy := map[string][]string{}
	for _, command := range set.Commands() {
		if command.Action.Kind != ActionNativeUI {
			continue
		}
		handler := command.Action.Handler
		declaredBy[handler] = append(declaredBy[handler], command.ID)
		if !den[handler] {
			problems = append(problems, fmt.Sprintf(
				"command %s references handler %q absent from the %s build inventory",
				command.ID, handler, command.Action.Kind))
		}
	}
	for handler, commands := range declaredBy {
		if len(commands) > 1 {
			sort.Strings(commands)
			problems = append(problems, fmt.Sprintf(
				"handler %q is declared by %d commands (%v); exactly one is allowed",
				handler, len(commands), commands))
		}
	}
	for _, id := range inventories.Den {
		if nonCommand[id] {
			continue
		}
		if len(declaredBy[id]) == 0 {
			problems = append(problems, fmt.Sprintf(
				"build handler %q is neither declared by an effective stock contribution nor classified as a non-command callback", id))
		}
	}

	sort.Strings(problems)
	out := make([]error, 0, len(problems))
	for _, p := range problems {
		out = append(out, fmt.Errorf("%s", p))
	}
	return out
}
