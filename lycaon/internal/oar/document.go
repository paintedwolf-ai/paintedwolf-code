package oar

import (
	"sort"
	"strings"
)

// CurrentOARVersion is the revision this engine implements.
const CurrentOARVersion = "1.0"

// SchemaBaseURL is the published $id base the OAR schemas carry.
const SchemaBaseURL = "https://openagentrules.org/spec/" + CurrentOARVersion + "/"

func capabilityHostFacts(cap *CapabilityDocument) map[string]struct{} {
	out := map[string]struct{}{}
	if cap == nil {
		return out
	}
	for _, hf := range cap.HostFacts {
		if name := strings.TrimSpace(hf.Name); name != "" {
			out[name] = struct{}{}
		}
	}
	return out
}

// RuleIdentity is the (namespace, id) pair that identifies a rule. An empty
// namespace means the loading host's own scope ([OAR-DOC-8]).
type RuleIdentity struct {
	Namespace string
	ID        string
}

// Identity returns the rule's (namespace, id) pair.
func (r *Rule) Identity() RuleIdentity {
	if r == nil {
		return RuleIdentity{}
	}
	return RuleIdentity{Namespace: r.Namespace, ID: r.ID}
}

// String renders the qualified identifier: "acme.security/RULE" or "RULE".
func (id RuleIdentity) String() string {
	if id.Namespace == "" {
		return id.ID
	}
	return id.Namespace + "/" + id.ID
}

// Qualified returns the rule's qualified identifier, which is what a counter is
// keyed by ([OAR-FIRE-5]) and what evaluation order sorts on ([OAR-EVAL-1]).
func (r *Rule) Qualified() string {
	return r.Identity().String()
}

// RequiresFor derives capability profiles and host-tier facts from a condition and selector.
func RequiresFor(when string, flow []string, selector Selector) Requires {
	tiers := map[string]FactTier{}
	profiles := map[string]string{}
	for _, d := range factDecls {
		name := publishedName(d.name, d.tier)
		tiers[name] = d.tier
		profiles[name] = d.profile
	}
	for _, f := range observationFns {
		name := publishedName(f.name, f.tier)
		tiers[name] = f.tier
		profiles[name] = f.profile
	}

	names := FactsReferenced(when, flow)
	names = append(names, selector.Clauses()...)

	var out Requires
	seenProfile := map[string]bool{}
	seenFact := map[string]bool{}
	for _, name := range names {
		switch tiers[name] {
		case FactTierStandard:
			if p := profiles[name]; p != "" && !seenProfile[p] {
				seenProfile[p] = true
				out.Profiles = append(out.Profiles, p)
			}
		case FactTierHost:
			if !seenFact[name] {
				seenFact[name] = true
				out.Facts = append(out.Facts, name)
			}
		case FactTierCore:
		}
	}
	sort.Strings(out.Profiles)
	sort.Strings(out.Facts)
	return out
}

// PortabilityReport lists the non-portable references in a rule.
type PortabilityReport struct {
	RuleID     string
	HostAnchor string   // non-empty when anchor: is a host-native id
	HostFacts  []string // host-tier facts and functions referenced
	Portable   bool
}

// LintPortability reports non-portable references without rejecting the rule ([OAR-PROF-8]).
func LintPortability(r *Rule) PortabilityReport {
	if r == nil {
		return PortabilityReport{}
	}
	rep := PortabilityReport{RuleID: r.ID, Portable: true}
	if !IsCoreAnchor(r.Anchor) {
		// A resolved anchor is a local id; a local id implementing a core anchor
		// still came from a portable rule.
		if InstalledCapabilityDocument().CoreAnchorFor(r.Anchor) == "" {
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

// referencedIdentifiers includes conditions, flow, selectors, and copy ([OAR-FACT-11], [OAR-COPY-5]).
func referencedIdentifiers(r *Rule) []string {
	names := FactsReferenced(r.When, r.Flow)
	names = append(names, r.Selector.Clauses()...)
	return append(names, copyRefs(r.Copy)...)
}
