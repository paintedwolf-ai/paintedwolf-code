package oar

import (
	"fmt"
	"github.com/lycaon/lycaon/internal/oarcore"
	"sort"
	"strings"

	"github.com/lycaon/lycaon/internal/guidance"
)

// Kind is the formal OAR rule tier.
type Kind string

const (
	KindSchema    Kind = "schema"
	KindPolicy    Kind = "policy"
	KindInvariant Kind = "invariant"
	KindDetector  Kind = "detector"
)

// kindOrder is Emit precedence: schema → policy → invariant → detector.
var kindOrder = map[Kind]int{
	KindSchema:    0,
	KindPolicy:    1,
	KindInvariant: 2,
	KindDetector:  3,
}

// Effect is the OAR Decision effect.
type Effect string

const (
	EffectBlock     Effect = "block"
	EffectWarn      Effect = "warn"
	EffectNudge     Effect = "nudge"
	EffectAllow     Effect = "allow"
	EffectTransform Effect = "transform"
)

// OnFireAction is the closed declared-effect vocabulary (schema SSOT).
type OnFireAction string

const (
	OnFireIncrementCounter OnFireAction = "increment_counter"
	OnFireResetCounter     OnFireAction = "reset_counter"
	OnFirePublishEvent     OnFireAction = "publish_event"
	OnFireIncrementBreaker OnFireAction = "increment_breaker"
	OnFireResetBreaker     OnFireAction = "reset_breaker"
)

// Selector narrows a rule to a subset of the occurrences of its anchor. Each
// clause is named by a fact and matches by set membership; clauses are
// conjunctive, and an omitted clause is permissive ([OAR-SEL-1]–[OAR-SEL-5]).
// An unknown clause name is a load error ([OAR-SEL-3]).
type Selector map[string][]string

// Clauses returns the clause names in sorted order, for stable diagnostics.
func (s Selector) Clauses() []string {
	out := make([]string, 0, len(s))
	for name := range s {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

// References holds interop taxonomy tags (telemetry/docs only).
type References struct {
	OWASPLLM   []string `yaml:"owasp_llm,omitempty" json:"owasp_llm,omitempty"`
	MITREAtlas []string `yaml:"mitre_atlas,omitempty" json:"mitre_atlas,omitempty"`
}

// DetectorRef delegates condition to the detector seam (kind: detector).
type DetectorRef struct {
	Ref string `yaml:"ref" json:"ref"`
}

// Tier identifies rule provenance.
type Tier string

const (
	TierBuiltin Tier = "builtin"
	TierPack    Tier = "pack"
)

// ValidateTier checks rule provenance.
func ValidateTier(t Tier) error {
	switch t {
	case TierBuiltin, TierPack:
		return nil
	default:
		return fmt.Errorf("unknown rule-pack tier %q", t)
	}
}

// Requires is what a rule declares it needs from the host, so its portability
// is computable from the document alone ([OAR-DOC-21], [OAR-FACT-20]).
type Requires struct {
	Profiles []string `yaml:"profiles,omitempty" json:"profiles,omitempty"`
	Facts    []string `yaml:"facts,omitempty" json:"facts,omitempty"`
}

// Copy is the closed presentation-text object. No member of it may affect
// selection, evaluation, ordering, precedence, or the returned decision: two
// rules differing only in Copy produce identical decisions ([OAR-DOC-24]).
type Copy struct {
	Title   string `yaml:"title,omitempty" json:"title,omitempty"`
	What    string `yaml:"what,omitempty" json:"what,omitempty"`
	Cause   string `yaml:"cause,omitempty" json:"cause,omitempty"`
	Why     string `yaml:"why,omitempty" json:"why,omitempty"`
	Fix     string `yaml:"fix,omitempty" json:"fix,omitempty"`
	Instead string `yaml:"instead,omitempty" json:"instead,omitempty"`
}

// Rule is one parsed OAR document.
type Rule struct {
	document  *oarcore.Rule
	OAR       string // the format marker and version ([OAR-DOC-3])
	ID        string
	Namespace string // publisher scope; empty means the loading host's own
	Requires  Requires
	Status    string
	Overrides []string
	// overrideTargets are Overrides resolved to loaded rule ids by
	// ResolveOverrides before evaluate.
	overrideTargets []string
	Title           string
	Kind            Kind
	// Anchor is the host-resolved lifecycle moment.
	Anchor      string
	Selector    Selector
	When        string
	Flow        []string
	Effect      Effect
	Enforcement string
	Mandatory   bool
	OnError     string
	// errorTarget is the substitute resolved at load.
	errorTarget *Rule
	OnFire      []OnFireAction
	// CounterScope names the fact that keys this rule's counters.
	CounterScope string
	References   References
	Detector     *DetectorRef
	Copy         Copy
	// Transform is present if and only if Effect is transform ([OAR-DOC-22]).
	Transform *TransformSpec

	// Audience narrows the tool profiles that can receive this rule when the
	// anchor and selector are broader than its `when` condition. Empty means
	// the anchor and selector decide.
	Audience []string
	// Scenarios are the authored conformance fixtures for this rule.
	Scenarios []guidance.ScenarioEntry

	// Emit, What, Cause, and Fix are rendering inputs.
	Emit   string
	What   string
	Cause  string
	Fix    string
	Tier   Tier   // provenance: builtin | project | pack
	Source string // path or pack name
}

// TransformSpec describes a content mutation ([OAR-OPS-13]).
type TransformSpec struct {
	Action      string
	Target      string
	Replacement string
	// HasReplacement distinguishes an absent replacement from an empty one.
	HasReplacement bool
}

// Advisory is one enforced same-effect fire on a nudge or warn decision
// ([OAR-EVAL-20]). Copy is rendered independently of the others ([OAR-COPY-9]).
// Data is this host's per-code reject payload for envelope rendering.
type Advisory struct {
	Code string
	Rule string
	Copy map[string]string
	Data map[string]any
}

// Decision is the engine outcome for one occurrence ([OAR-EVAL-8]).
type Decision struct {
	Effect Effect
	Code   string
	// Rule is the qualified identifier ([OAR-DOC-8]): namespace/id, or the
	// bare id when the document has no namespace.
	Rule   string
	Data   map[string]any
	OnFire []OnFireAction
	// Advisories is every enforced rule that fired with the winning advisory
	// effect, in evaluation order. Empty when the decision is block, transform,
	// or none ([OAR-EVAL-20]). The first item is the named decision
	// ([OAR-EVAL-19]).
	Advisories []Advisory
	// Copy is the rendered copy of the rule whose identifiers this decision
	// carries ([OAR-COPY-7]). Substitution does not change the decision
	// ([OAR-COPY-6], [OAR-DOC-24]).
	Copy map[string]string
}

// RuleSet is the loaded + validated rules indexed for Emit ordering.
type RuleSet struct {
	byID    map[string]*Rule
	byOn    map[string][]*Rule
	ordered []*Rule
}

// NewRuleSet indexes rules by id and on-anchor with Emit ordering.
func NewRuleSet(rules []*Rule) *RuleSet {
	rs := &RuleSet{
		byID: make(map[string]*Rule, len(rules)),
		byOn: make(map[string][]*Rule),
	}
	ordered := append([]*Rule(nil), rules...)
	sort.SliceStable(ordered, func(i, j int) bool {
		a, b := ordered[i], ordered[j]
		oa, ob := kindOrder[a.Kind], kindOrder[b.Kind]
		if oa != ob {
			return oa < ob
		}
		// [OAR-EVAL-1] ties break on the *qualified* identifier, compared by
		// Unicode code point. Sorting on the bare id would let two publishers'
		// rules interleave differently on two engines.
		return a.Qualified() < b.Qualified()
	})
	for _, r := range ordered {
		rs.byID[r.Qualified()] = r
		rs.byOn[r.Anchor] = append(rs.byOn[r.Anchor], r)
	}
	rs.ordered = ordered
	return rs
}

// Get returns a rule by bare id or qualified identifier.
func (rs *RuleSet) Get(id string) (*Rule, bool) {
	if rs == nil {
		return nil, false
	}
	r, ok := rs.byID[id]
	return r, ok
}

// RejectDuplicateIdentities refuses a set that contains two rules with the
// same qualified identifier ([OAR-DOC-9]).
func RejectDuplicateIdentities(rs *RuleSet) error {
	if rs == nil {
		return nil
	}
	seen := make(map[string]bool, rs.Len())
	for _, r := range rs.All() {
		if r == nil {
			continue
		}
		q := r.Qualified()
		if seen[q] {
			return fmt.Errorf("[OAR-DOC-9] rule set contains two rules with qualified identifier %s", q)
		}
		seen[q] = true
	}
	return nil
}

// RejectUnknownErrorSubstitutes refuses an on_error that names a rule that
// is not in the loaded set ([OAR-OPS-5]).
func RejectUnknownErrorSubstitutes(rs *RuleSet) error {
	if rs == nil {
		return nil
	}
	for _, r := range rs.All() {
		if r == nil {
			continue
		}
		switch strings.TrimSpace(r.OnError) {
		case "", "fail_closed", "fail_open":
			continue
		}
		want := strings.TrimSpace(r.OnError)
		if !strings.Contains(want, "/") && r.Namespace != "" {
			want = r.Namespace + "/" + want
		}
		target, ok := rs.Get(want)
		if !ok || target.Qualified() != want {
			return fmt.Errorf("[OAR-OPS-5] rule %s names on_error %s, which is not loaded", r.Qualified(), want)
		}
		r.errorTarget = target
	}
	return nil
}

// OnAnchor returns rules bound to anchor id in Emit order.
func (rs *RuleSet) OnAnchor(anchor string) []*Rule {
	if rs == nil {
		return nil
	}
	return rs.byOn[anchor]
}

// All returns every rule in Emit order.
func (rs *RuleSet) All() []*Rule {
	if rs == nil {
		return nil
	}
	return rs.ordered
}

// Len returns the rule count.
func (rs *RuleSet) Len() int {
	if rs == nil {
		return 0
	}
	return len(rs.ordered)
}

// ParseOnFire parses a closed on_fire action string.
func ParseOnFire(s string) (OnFireAction, error) {
	a := OnFireAction(strings.TrimSpace(s))
	switch a {
	case OnFireIncrementCounter, OnFireResetCounter, OnFirePublishEvent, OnFireIncrementBreaker, OnFireResetBreaker:
		return a, nil
	default:
		return "", fmt.Errorf("unknown on_fire action %q", s)
	}
}
