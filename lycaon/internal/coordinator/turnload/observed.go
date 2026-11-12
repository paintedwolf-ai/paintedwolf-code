package turnload

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
