package oar

import (
	"fmt"
	"sort"

	"github.com/lycaon/lycaon/internal/oarcopy"
)

func validateCatalogueTiers() error {
	facts := map[string]FactTier{}
	for _, d := range factDecls {
		switch d.tier {
		case FactTierCore, FactTierStandard, FactTierHost:
		default:
			return fmt.Errorf("fact %q has no tier", d.name)
		}
		if (d.tier == FactTierStandard) != (d.profile != "") {
			return fmt.Errorf("fact %q tier %q and profile %q disagree", d.name, d.tier, d.profile)
		}
		facts[d.name] = d.tier
	}
	fns := map[string]FactTier{}
	for _, f := range observationFns {
		switch f.tier {
		case FactTierCore, FactTierStandard, FactTierHost:
		default:
			return fmt.Errorf("fact %q has no tier", f.name)
		}
		fns[f.name] = f.tier
	}
	for _, name := range requiredCoreFacts {
		tier, ok := facts[name]
		if !ok {
			return fmt.Errorf("core fact %q is not in FactCatalogue()", name)
		}
		if tier != FactTierCore {
			return fmt.Errorf("core fact %q is declared %q", name, tier)
		}
	}
	for _, name := range requiredCoreFunctions {
		tier, ok := fns[name]
		if !ok {
			return fmt.Errorf("core fact %q is not in FactCatalogue()", name)
		}
		if tier != FactTierCore {
			return fmt.Errorf("core fact %q is declared %q", name, tier)
		}
	}
	for profile, members := range standardProfileFacts {
		for _, name := range members {
			tier, ok := facts[name]
			if !ok {
				if _, isFn := fns[name]; isFn {
					continue
				}
				return fmt.Errorf("standard profile %q member %q is not in FactCatalogue()", profile, name)
			}
			if tier != FactTierStandard {
				return fmt.Errorf("standard profile %q member %q is declared %q", profile, name, tier)
			}
		}
	}
	return nil
}

var (
	requiredCoreFacts     = oarcopy.RequiredCoreFacts()
	requiredCoreFunctions = oarcopy.RequiredCoreFunctions()
)

// portabilityReport lists the non-portable references in a rule.
type portabilityReport struct {
	RuleID     string
	HostAnchor string   // non-empty when anchor: is a host-native id
	HostFacts  []string // host-tier facts and functions referenced
	Portable   bool
}

func lintPortability(r *Rule) portabilityReport {
	if r == nil {
		return portabilityReport{}
	}
	rep := portabilityReport{RuleID: r.ID, Portable: true}
	if !IsCoreAnchor(r.Anchor) {
		// A resolved anchor is a local id; a local id implementing a core anchor
		// still came from a portable rule.
		if InstalledEngineDeclaration().CoreAnchorFor(r.Anchor) == "" {
			rep.HostAnchor = r.Anchor
			rep.Portable = false
		}
	}
	tiers := map[string]FactTier{}
	eachDeclaredFact(func(d factDecl) {
		tiers[publishedName(d.name, d.tier)] = d.tier
	})
	for _, f := range observationFns {
		tiers[publishedName(f.name, f.tier)] = f.tier
	}
	seen := map[string]bool{}
	for _, name := range referencedIdentifiers(r) {
		if tiers[name] != FactTierHost || seen[name] {
			continue
		}
		seen[name] = true
		rep.HostFacts = append(rep.HostFacts, name)
		rep.Portable = false
	}
	sort.Strings(rep.HostFacts)
	return rep
}

// referencedIdentifiers includes conditions, flow, selectors, and copy ([OAR-FACT-9], [OAR-COPY-5]).
func referencedIdentifiers(r *Rule) []string {
	names := FactsReferenced(r.When, r.Flow)
	names = append(names, r.Selector.Clauses()...)
	return append(names, copyRefs(r.Copy)...)
}
