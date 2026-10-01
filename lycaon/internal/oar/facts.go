package oar

import (
	"github.com/lycaon/lycaon/internal/oarcore"
	"sort"
	"strings"
)

// catalogueFacts is the closed set of fact names a rule may reference, keyed by
// the published name: bare for core and profile facts, lycaon.<fact> for host
// facts ([OAR-FACT-18]). Built from the declaration table.
var (
	catalogueFacts       map[string]struct{}
	publishedFacts       map[string]struct{}
	selectorFactTypes    map[string]string
	declaredFactTypes    map[string]string
	declaredFactTiers    map[string]FactTier
	declaredFactProfiles map[string]string
)

func init() {
	catalogueFacts = buildCatalogueFacts()
	publishedFacts = make(map[string]struct{}, len(factDecls)+len(occurrenceFactDecls)+len(observationFns))
	selectorFactTypes = make(map[string]string, len(factDecls))
	declaredFactTypes = make(map[string]string, len(factDecls)+len(occurrenceFactDecls))
	declaredFactTiers = make(map[string]FactTier, len(factDecls)+len(occurrenceFactDecls)+len(observationFns))
	declaredFactProfiles = make(map[string]string, len(declaredFactTiers))
	for _, d := range factDecls {
		selectorFactTypes[publishedName(d.name, d.tier)] = factTypeString(d.typ)
	}
	eachDeclaredFact(func(d factDecl) {
		name := publishedName(d.name, d.tier)
		publishedFacts[name] = struct{}{}
		declaredFactTypes[name] = factTypeString(d.typ)
		declaredFactTiers[name] = d.tier
		declaredFactProfiles[name] = d.profile
	})
	for _, f := range observationFns {
		name := publishedName(f.name, f.tier)
		publishedFacts[name] = struct{}{}
		declaredFactTiers[name] = f.tier
		declaredFactProfiles[name] = f.profile
	}
}

// eachDeclaredFact visits catalogue facts and copy-only occurrence slots
// ([OAR-FACT-11], [OAR-FACT-20]).
func eachDeclaredFact(fn func(factDecl)) {
	for _, d := range factDecls {
		fn(d)
	}
	for _, d := range occurrenceFactDecls {
		fn(d)
	}
}

func buildCatalogueFacts() map[string]struct{} {
	out := make(map[string]struct{}, len(factDecls)+len(observationFns))
	for _, d := range factDecls {
		out[publishedName(d.name, d.tier)] = struct{}{}
	}
	for _, f := range observationFns {
		out[publishedName(f.name, f.tier)] = struct{}{}
	}
	return out
}

// expensiveFacts require lazy providers (evidence / ledger).
var expensiveFacts = map[string]struct{}{
	"paintedwolf.unobserved_cited_paths": {}, "paintedwolf.unobserved_cited_urls": {},
	"paintedwolf.unobserved_cited_handles": {}, "paintedwolf.citation_fields_present": {},
	"paintedwolf.claims_completion": {}, "paintedwolf.has_matching_ledger_job": {},
	"paintedwolf.ledger_criteria_met": {}, "paintedwolf.worker_summary_present": {},
	"paintedwolf.worker_artifact_present": {}, "paintedwolf.files_touched": {},
	"paintedwolf.summary_length": {},
	// Git index comparisons run only for a rule that reads them.
	"paintedwolf.worktree_stale_paths": {}, "paintedwolf.worktree_leftover_paths": {},
	"paintedwolf.worktree_conflict_paths": {},
}

// FactsReferenced returns catalogue fact names appearing in when / flow text.
func FactsReferenced(when string, flow []string) []string {
	seen := map[string]struct{}{}
	var out []string
	add := func(name string) {
		if _, ok := catalogueFacts[name]; !ok {
			return
		}
		if _, ok := seen[name]; ok {
			return
		}
		seen[name] = struct{}{}
		out = append(out, name)
	}
	scanIdents(when, add)
	for _, step := range flow {
		scanIdents(step, add)
	}
	// Counter facts always needed when on_fire may run — callers add separately.
	return out
}

// RuleFactsReferenced returns every runtime fact reference ([OAR-FACT-11]).
func RuleFactsReferenced(r *Rule) []string {
	if r == nil {
		return nil
	}
	if r.document != nil {
		names := make([]string, 0, len(r.document.Refs))
		for name := range r.document.Refs {
			names = append(names, name)
		}
		sort.Strings(names)
		return names
	}
	seen := map[string]bool{}
	var out []string
	add := func(name string) {
		name = strings.TrimSpace(name)
		if name == "" || seen[name] {
			return
		}
		if _, ok := catalogueFacts[name]; !ok {
			if !DeclaresCopyFact(name) {
				return
			}
		}
		seen[name] = true
		out = append(out, name)
	}
	for _, name := range FactsReferenced(r.When, r.Flow) {
		add(name)
	}
	for _, name := range r.Selector.Clauses() {
		add(name)
	}
	add(r.CounterScope)
	if r.Transform != nil && r.Transform.Target != "content" {
		add(r.Transform.Target)
	}
	for _, name := range copyRefs(r.Copy) {
		add(name)
	}
	if len(r.OnFire) > 0 {
		add("fire_count")
		add("breaker_count")
	}
	return out
}

func scanIdents(expr string, add func(string)) {
	if strings.TrimSpace(expr) == "" {
		return
	}
	names, err := oarcore.ConditionReferences(expr)
	if err != nil {
		return
	}
	for _, name := range names {
		add(name)
	}
}

// AssembleFacts runs registered providers for the named facts (lazy).
// Cheap structural facts on gc are assumed already set by the caller.
func AssembleFacts(gc *GuardContext, names []string) error {
	if gc == nil {
		return nil
	}
	for _, name := range names {
		if err := gc.Ensure(name); err != nil {
			return err
		}
	}
	return nil
}
