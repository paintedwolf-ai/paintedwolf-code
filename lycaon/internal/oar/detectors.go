package oar

import (
	"fmt"
	"strings"
)

// Finding is a detector observation evaluated by a rule condition.
type Finding struct {
	Fact  string  // catalogue fact name
	Value any     // structured detector output
	Score float64 // numeric detector score
}

// Detector is the content-safety fact-provider seam.
// Name is the short id; registry keys are detector://<name>.
type Detector interface {
	Name() string
	Inspect(gc *GuardContext) ([]Finding, error)
}

// DetectorURI builds or normalizes a detector:// ref.
func DetectorURI(nameOrRef string) string {
	s := strings.TrimSpace(nameOrRef)
	if s == "" {
		return ""
	}
	if strings.HasPrefix(s, "detector://") {
		return s
	}
	return "detector://" + s
}

// DetectorRegistry maps detector:// refs to implementations.
type DetectorRegistry struct {
	byRef map[string]Detector
}

// NewDetectorRegistry returns an empty registry.
func NewDetectorRegistry() *DetectorRegistry {
	return &DetectorRegistry{byRef: map[string]Detector{}}
}

// Register adds a detector under detector://<Name()>.
func (r *DetectorRegistry) Register(d Detector) {
	if r == nil || d == nil {
		return
	}
	if r.byRef == nil {
		r.byRef = map[string]Detector{}
	}
	r.byRef[DetectorURI(d.Name())] = d
}

// Lookup returns a detector by ref or short name, or nil.
func (r *DetectorRegistry) Lookup(ref string) Detector {
	if r == nil || r.byRef == nil {
		return nil
	}
	return r.byRef[DetectorURI(ref)]
}

// Dispatch runs a detector for a kind:detector rule. Its error reaches the
// rule's on_error in the pipeline.
func (r *DetectorRegistry) Dispatch(ref string, gc *GuardContext) ([]Finding, error) {
	if r == nil {
		return nil, fmt.Errorf("detector registry unset")
	}
	uri := DetectorURI(ref)
	if uri == "" {
		return nil, fmt.Errorf("detector ref required")
	}
	d := r.Lookup(uri)
	if d == nil {
		return nil, fmt.Errorf("detector %q not registered", uri)
	}
	return d.Inspect(gc)
}

// ApplyFindings writes detector observation facts onto gc.
func ApplyFindings(gc *GuardContext, findings []Finding) {
	if gc == nil {
		return
	}
	if gc.Published == nil {
		gc.Published = map[string]any{}
	}
	for _, f := range findings {
		raw := f.Value
		if raw == nil && (f.Fact == "prompt_injection_score" || f.Fact == "jailbreak_score") {
			raw = f.Score
		}
		gc.Published[f.Fact] = raw
		switch f.Fact {
		case "prompt_injection_score":
			gc.Content.PromptInjectionScore = f.Score
		case "jailbreak_score":
			gc.Content.JailbreakScore = f.Score
		case "pii_entities":
			if f.Value != nil {
				if list, ok := f.Value.([]any); ok {
					gc.Content.PIIEntities = list
				} else {
					gc.Content.PIIEntities = []any{f.Value}
				}
			}
		case "secret_matches":
			if f.Value != nil {
				if list, ok := f.Value.([]any); ok {
					gc.Content.SecretMatches = list
				} else {
					gc.Content.SecretMatches = []any{f.Value}
				}
			}
		}
	}
}

type detectorFactState struct {
	promptInjectionScore float64
	jailbreakScore       float64
	piiEntities          []any
	secretMatches        []any
	published            map[string]detectorPublishedFact
}

type detectorPublishedFact struct {
	value   any
	present bool
}

func captureDetectorFacts(gc *GuardContext) detectorFactState {
	if gc == nil {
		return detectorFactState{}
	}
	state := detectorFactState{
		promptInjectionScore: gc.Content.PromptInjectionScore,
		jailbreakScore:       gc.Content.JailbreakScore,
		piiEntities:          append([]any(nil), gc.Content.PIIEntities...),
		secretMatches:        append([]any(nil), gc.Content.SecretMatches...),
		published:            make(map[string]detectorPublishedFact, 4),
	}
	for _, name := range detectorFactNames {
		value, present := gc.Published[name]
		state.published[name] = detectorPublishedFact{value: value, present: present}
	}
	return state
}

func restoreDetectorFacts(gc *GuardContext, state detectorFactState) {
	if gc == nil {
		return
	}
	gc.Content.PromptInjectionScore = state.promptInjectionScore
	gc.Content.JailbreakScore = state.jailbreakScore
	gc.Content.PIIEntities = state.piiEntities
	gc.Content.SecretMatches = state.secretMatches
	for _, name := range detectorFactNames {
		prior := state.published[name]
		if prior.present {
			if gc.Published == nil {
				gc.Published = map[string]any{}
			}
			gc.Published[name] = prior.value
		} else {
			delete(gc.Published, name)
		}
	}
}

var detectorFactNames = []string{
	"prompt_injection_score",
	"jailbreak_score",
	"pii_entities",
	"secret_matches",
	"moderation_categories",
	"moderation_score",
}
