package gotreesitter

// erlangMacroCallExprConflictChoice resolves macro invocation forks in favor of the C-faithful args shift.
func erlangMacroCallExprConflictChoice(lang *Language, actions []ParseAction) (ParseAction, bool) {
	if lang == nil || len(actions) < 2 {
		return ParseAction{}, false
	}
	var shift ParseAction
	haveShift := false
	haveReduce := false
	for _, act := range actions {
		switch act.Type {
		case ParseActionShift:
			if haveShift || act.Repetition {
				return ParseAction{}, false
			}
			shift = act
			haveShift = true
		case ParseActionReduce:
			if act.ChildCount != 2 || !symbolHasName(lang, act.Symbol, "macro_call_expr") {
				return ParseAction{}, false
			}
			haveReduce = true
		default:
			return ParseAction{}, false
		}
	}
	if !haveShift || !haveReduce {
		return ParseAction{}, false
	}
	return shift, true
}
