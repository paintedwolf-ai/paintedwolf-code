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
	EffectBlock Effect = "block"
	EffectWarn  Effect = "warn"
	EffectNudge Effect = "nudge"
	EffectAllow Effect = "allow"
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
// conjunctive, and an omitted clause is permissive ([OAR-SEL-3]).
// An unknown clause name is a load error ([OAR-SEL-7]).
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

// Copy is a rule's presentation text. No member of it may affect selection,
// evaluation, ordering, precedence, or the returned decision ([OAR-DOC-18],
// [OAR-COPY-6]).
type Copy struct {
	Title   string `yaml:"title,omitempty" json:"title,omitempty"`
	What    string `yaml:"what,omitempty" json:"what,omitempty"`
	Cause   string `yaml:"cause,omitempty" json:"cause,omitempty"`
	Why     string `yaml:"why,omitempty" json:"why,omitempty"`
	Fix     string `yaml:"fix,omitempty" json:"fix,omitempty"`
	Instead string `yaml:"instead,omitempty" json:"instead,omitempty"`
	Message string `yaml:"message,omitempty" json:"message,omitempty"`
}

// Rule is one parsed OAR document.
type Rule struct {
	document *oarcore.Rule
	raw      map[string]any // the authored document
	// Disabled is set by operator configuration, never by the document.
	Disabled    bool
	SpecVersion string // the document's spec_version ([OAR-DOC-3])
	ID          string
	Namespace   string // publisher scope; empty means the loading host's own
	// RequiresFacts are the facts and functions the rule reads beyond the core
	// tier ([OAR-DOC-22]).
	RequiresFacts []string
	Severity      string
	Title         string
	Kind          Kind
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
	References  References
	Detector    *DetectorRef
	Copy        Copy

	// Audience narrows the tool profiles that can receive this rule when the
	// anchor and selector are broader than its `when` condition. Empty means
	// the anchor and selector decide.
	Audience []string
	// NextSteps is the host catalog entry for this rule. It never reaches a decision.
	NextSteps *NextSteps
	// Scenarios are the authored conformance fixtures for this rule.
	Scenarios []guidance.ScenarioEntry

	// Emit is the provenance channel the rendering uses.
	Emit   string
	Tier   Tier   // provenance: builtin | project | pack
	Source string // path or pack name
}

// Decision is the engine outcome for one occurrence ([OAR-EVAL-6]).
type Decision struct {
	Effect Effect
	Code   string
	// Rule is the qualified identifier ([OAR-DOC-22]): namespace/id, or the
	// bare id when the document has no namespace.
	Rule   string
	Data   map[string]any
	OnFire []OnFireAction
	// Copy is the rendered copy of the rule whose identifiers this decision
	// carries ([OAR-COPY-7]). Substitution does not change the decision
	// ([OAR-COPY-6], [OAR-DOC-18]).
	Copy map[string]string
}

// RuleSet is the loaded + validated rules indexed for Emit ordering.
type RuleSet struct {
	byID    map[string]*Rule
	byOn    map[string][]*Rule
	ordered []*Rule
	// hints are the rule set's hint documents, loaded and never evaluated
	// ([OAR-HINT-1]).
	hints []*oarcore.Hint
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
		// [OAR-EVAL-1] then id, then namespace with an absent namespace first,
		// compared by code point (UTF-8 byte order agrees).
		if a.ID != b.ID {
			return a.ID < b.ID
		}
		return a.Namespace < b.Namespace
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
// bindErrorSubstitutes binds each on_error substitute to the rule it names in
// the same namespace, whose copy the substituted block carries ([OAR-OPS-4]).
// The rule-set check has already refused a substitute that names no rule.
func bindErrorSubstitutes(rs *RuleSet) {
	for _, r := range rs.All() {
		r.errorTarget = nil
		if want := errorSubstitute(r); want != "" {
			r.errorTarget, _ = rs.Get(want)
		}
	}
}

// errorSubstitute is the qualified identifier a rule's on_error substitutes,
// or empty when on_error is fail_closed or fail_open.
func errorSubstitute(r *Rule) string {
	want := strings.TrimSpace(r.OnError)
	switch want {
	case "", "fail_closed", "fail_open":
		return ""
	}
	return oarcore.Identity(r.Namespace, want)
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
