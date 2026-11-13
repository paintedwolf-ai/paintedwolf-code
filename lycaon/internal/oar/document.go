package oar

import (
	"github.com/lycaon/lycaon/internal/oarcore"
	"sort"
	"strings"
)

// CurrentOARVersion is the revision this engine implements.
const CurrentOARVersion = "1.0"

// SchemaBaseURL is the published $id base the OAR schemas carry.
const SchemaBaseURL = "https://openagentrules.org/spec/" + CurrentOARVersion + "/"

func capabilityHostFacts(cap *EngineDeclaration) map[string]struct{} {
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
// namespace means the loading host's own scope ([OAR-DOC-22]).
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

// Qualified returns the rule's identity as one string, which counters and the
// trace are keyed by ([OAR-FIRE-4]).
func (r *Rule) Qualified() string {
	return r.Identity().String()
}

// Tools are the tool names the rule's selector admits; empty admits every tool.
func (r *Rule) Tools() []string {
	return r.Selector["tools"]
}

// Operations are the paintedwolf.operation values the rule's condition
// requires: the native operation beneath command, or the structured tool of
// that name.
func (r *Rule) Operations() []string {
	return oarcore.ConjunctValues(r.When, "paintedwolf.operation")
}

// RequiresFor derives requires_facts: every fact a condition, flow and
// selector reference beyond the core tier ([OAR-DOC-23], [OAR-PROF-5]).
func RequiresFor(when string, flow []string, selector Selector) []string {
	tiers := map[string]FactTier{}
	for _, d := range factDecls {
		tiers[publishedName(d.name, d.tier)] = d.tier
	}
	for _, f := range observationFns {
		tiers[publishedName(f.name, f.tier)] = f.tier
	}
	names := FactsReferenced(when, flow)
	names = append(names, selector.Clauses()...)
	seen := map[string]bool{}
	var out []string
	for _, name := range names {
		if t, declared := tiers[name]; declared && t != FactTierCore && !seen[name] {
			seen[name] = true
			out = append(out, name)
		}
	}
	sort.Strings(out)
	return out
}

// PortabilityReport lists the non-portable references in a rule.
type PortabilityReport struct {
	RuleID     string
	HostAnchor string   // non-empty when anchor: is a host-native id
	HostFacts  []string // host-tier facts and functions referenced
	Portable   bool
}

// LintPortability reports non-portable references without rejecting the rule ([OAR-PROF-7]).
func LintPortability(r *Rule) PortabilityReport {
	if r == nil {
		return PortabilityReport{}
	}
	rep := PortabilityReport{RuleID: r.ID, Portable: true}
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
