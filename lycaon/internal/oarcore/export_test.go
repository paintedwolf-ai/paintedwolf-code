package oarcore

// LoadRuleDocument loads one rule document against a declared environment. A
// hint document is refused here: it is not a rule.
func LoadRuleDocument(doc any, env *Environment) (*Rule, error) {
	rule, hint, err := LoadDocument(doc, env)
	if err != nil {
		return nil, err
	}
	if hint != nil {
		return nil, loadErr("[OAR-DOC-2] document %s has kind hint and is not a rule", hint.ID)
	}
	return rule, nil
}
