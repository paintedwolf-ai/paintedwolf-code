package oar

import (
	"fmt"
	"github.com/lycaon/lycaon/internal/oarcore"
	"sort"
	"strings"
)

// RequireResolvableCounterReads rejects a rule set in which a counter read
// names no loaded rule ([OAR-FIRE-11]). This runs over the assembled set rather
// than per rule: the rule being read is normally a sibling in the same load, so
// resolvability is only decidable once every rule is present.
//
// A read resolves against the bare id or the qualified `namespace/id`, so a rule
// may read a sibling without repeating its own namespace.
func RequireResolvableCounterReads(rs *RuleSet) error {
	if rs == nil {
		return nil
	}
	known := map[string]bool{}
	for _, r := range rs.All() {
		known[r.Qualified()] = true
	}
	var missing []string
	for _, r := range rs.All() {
		refs, err := oarcore.CounterReferences(r.When, r.Namespace)
		if err != nil {
			return err
		}
		if r.document != nil {
			refs = r.document.CounterRefs
		}
		for _, ref := range refs {
			target := ref.Target
			if !known[target] {
				missing = append(missing, fmt.Sprintf("%s in %s names no loaded rule: %s", ref.Fn, r.Qualified(), target))
			}
			referenced := resolveCounterTarget(rs, ref.Literal, r.Namespace)
			if referenced != nil && referenced.CounterScope != "" && r.document != nil {
				decl, ok := r.document.FactDeclaration(referenced.CounterScope)
				if !ok {
					return fmt.Errorf("[OAR-FIRE-11] scope %s is not declared", referenced.CounterScope)
				}
				if decl.Tier == "profile" && !containsExact(r.Requires.Profiles, decl.Profile) {
					return fmt.Errorf("[OAR-FIRE-11] indirect scope %s requires profile %s", referenced.CounterScope, decl.Profile)
				}
				if decl.Tier == "host" && !containsExact(r.Requires.Facts, referenced.CounterScope) {
					return fmt.Errorf("[OAR-FIRE-11] indirect scope requires fact %s", referenced.CounterScope)
				}
			}
		}
	}
	if len(missing) == 0 {
		return nil
	}
	sort.Strings(missing)
	return fmt.Errorf("%s", strings.Join(missing, "; "))
}
