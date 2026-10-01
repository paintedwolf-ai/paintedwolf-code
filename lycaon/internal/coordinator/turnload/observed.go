package turnload

import "github.com/lycaon/lycaon/internal/promptunit"

// ObservedKind is the kind a turn had, read from the tools it called: what the
// agent did, never what it said.
func ObservedKind(called []string) string {
	if len(called) == 0 {
		return KindAnswerOnly
	}
	set := make(map[string]bool, len(called))
	for _, name := range called {
		set[name] = true
	}
	switch {
	case set["task"] || set["delegate_dispatch"]:
		return KindDelegate
	case set["write"] || set["edit"] || set["replace_lines"] || set["code_rewrite"] || set["jq_edit"] || set["delete"] || set["move"] || set["copy"]:
		return KindChange
	case set["command"] || set["verify"] || set["terminal_open"]:
		return KindRun
	default:
		return KindInspect
	}
}

// GuideLabels labels the instruction units a turn scored from the tools it
// called: a unit was needed when the turn called a tool it attaches to or
// one it is needed with. A unit that names neither maps to nil.
func GuideLabels(scored []promptunit.Unit, used []string) map[string]*bool {
	usedSet := make(map[string]bool, len(used))
	for _, name := range used {
		usedSet[name] = true
	}
	out := make(map[string]*bool, len(scored))
	for _, u := range scored {
		if !u.Labelled() {
			out[u.ID] = nil
			continue
		}
		needed := u.NeededBy(usedSet)
		out[u.ID] = &needed
	}
	return out
}
