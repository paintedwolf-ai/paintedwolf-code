package oarcore

// CheckRuleSet runs the load-time checks that span the documents of one rule
// set ([OAR-DOC-27]): unique rule identities ([OAR-DOC-28]), on_error
// substitutes and counter reads that name a rule of the reader's namespace
// ([OAR-OPS-4], [OAR-FIRE-6]), and unique hint codes ([OAR-HINT-2]).
func CheckRuleSet(rules []*Rule, hints []*Hint) error {
	byIdentity := make(map[string]*Rule, len(rules))
	for _, rule := range rules {
		if _, seen := byIdentity[rule.Qualified]; seen {
			return loadErr("[OAR-DOC-28] rule set holds two rules with identity %s", rule.Qualified)
		}
		byIdentity[rule.Qualified] = rule
	}
	for _, rule := range rules {
		if rule.OnError != "fail_closed" && rule.OnError != "fail_open" {
			if _, loaded := byIdentity[Identity(rule.Namespace, rule.OnError)]; !loaded {
				return loadErr("[OAR-OPS-4] rule %s on_error names %s, which is not a rule of its namespace in the rule set", rule.Qualified, rule.OnError)
			}
		}
		for _, id := range rule.CounterReads {
			if _, loaded := byIdentity[Identity(rule.Namespace, id)]; !loaded {
				return loadErr("[OAR-FIRE-6] rule %s reads the counters of %s, which is not a rule of its namespace in the rule set", rule.Qualified, id)
			}
		}
	}
	codes := make(map[string]bool, len(hints))
	for _, hint := range hints {
		if codes[hint.ID] {
			return loadErr("[OAR-HINT-2] rule set holds two hints with code %s", hint.ID)
		}
		codes[hint.ID] = true
	}
	return nil
}
