package definition

import (
	"fmt"
	"slices"
	"sort"
	"strings"
)

// A workflow rates its outcome from declared factual questions about each
// rated finding. The agent answers; the host looks up the level.

// BriefUnknown is the answer a dimension accepts when it declares that an
// answer may be unknown. The host rates an unknown both ways.
const BriefUnknown = "unknown"

// Brief tones a level can declare. They order nothing; a level's position in
// the declared list is its severity.
const (
	BriefToneCritical = "critical"
	BriefToneHigh     = "high"
	BriefToneMedium   = "medium"
	BriefToneLow      = "low"
	BriefToneGood     = "good"
	BriefToneNeutral  = "neutral"
)

// Brief is a workflow's declared rating: the question a reader asks, the facts
// each rated finding must answer, and the levels those answers decide.
type Brief struct {
	Question   string
	Dimensions []BriefDimension
	// Levels run most severe first. The last level has no conditions: it is
	// what a run with nothing matching any other level states.
	Levels []BriefLevel
	// Basis names the dimensions whose phrases say why the deciding finding
	// set the level.
	Basis []string
}

// BriefDimension is one fact every rated finding answers.
type BriefDimension struct {
	ID       string
	Label    string
	Question string
	Values   []BriefValue
	// AllowUnknown admits BriefUnknown, which the host rates as every value.
	AllowUnknown bool
}

// BriefValue is one declared answer.
type BriefValue struct {
	ID    string
	Label string
	// Phrase says what this answer means to a reader, for the basis line.
	Phrase string
}

// BriefLevel is one step of the scale.
type BriefLevel struct {
	Label  string
	Answer string
	Means  string
	Tone   string
	// When lists alternative conditions; a finding meets the level when every
	// dimension a condition names holds one of the listed answers.
	When []map[string][]string
}

// BriefRating is the host's rating of a set of findings.
type BriefRating struct {
	// Worst and Best are level indexes. They differ when an unknown answer
	// could decide either level; Worst is never less severe than Best.
	Worst int
	Best  int
	// Decider is the index of the finding that set Worst, or -1 when none
	// met any level above the default.
	Decider int
	Rated   int
}

// Settled reports whether every unknown answer leads to the same level.
func (r BriefRating) Settled() bool { return r.Worst == r.Best }

type briefYAML struct {
	Question   string               `yaml:"question"`
	Dimensions []briefDimensionYAML `yaml:"dimensions"`
	Levels     []briefLevelYAML     `yaml:"levels"`
	Basis      []string             `yaml:"basis,omitempty"`
}

type briefDimensionYAML struct {
	ID           string           `yaml:"id"`
	Label        string           `yaml:"label"`
	Question     string           `yaml:"question"`
	AllowUnknown bool             `yaml:"allow_unknown,omitempty"`
	Values       []briefValueYAML `yaml:"values"`
}

type briefValueYAML struct {
	ID     string `yaml:"id"`
	Label  string `yaml:"label"`
	Phrase string `yaml:"phrase,omitempty"`
}

type briefLevelYAML struct {
	Label  string                `yaml:"label"`
	Answer string                `yaml:"answer,omitempty"`
	Means  string                `yaml:"means"`
	Tone   string                `yaml:"tone,omitempty"`
	When   []map[string][]string `yaml:"when,omitempty"`
}

func parseBriefYAML(raw *briefYAML) (*Brief, error) {
	if raw == nil {
		return nil, nil
	}
	b := &Brief{Question: strings.TrimSpace(raw.Question)}
	for _, d := range raw.Dimensions {
		dim := BriefDimension{
			ID:           strings.TrimSpace(d.ID),
			Label:        strings.TrimSpace(d.Label),
			Question:     strings.TrimSpace(d.Question),
			AllowUnknown: d.AllowUnknown,
		}
		for _, v := range d.Values {
			dim.Values = append(dim.Values, BriefValue{
				ID:     strings.TrimSpace(v.ID),
				Label:  strings.TrimSpace(v.Label),
				Phrase: strings.TrimSpace(v.Phrase),
			})
		}
		b.Dimensions = append(b.Dimensions, dim)
	}
	for _, l := range raw.Levels {
		level := BriefLevel{
			Label:  strings.TrimSpace(l.Label),
			Answer: strings.TrimSpace(l.Answer),
			Means:  strings.TrimSpace(l.Means),
			Tone:   strings.TrimSpace(l.Tone),
		}
		for _, cond := range l.When {
			clean := map[string][]string{}
			for dim, values := range cond {
				for _, v := range values {
					clean[strings.TrimSpace(dim)] = append(clean[strings.TrimSpace(dim)], strings.TrimSpace(v))
				}
			}
			level.When = append(level.When, clean)
		}
		b.Levels = append(b.Levels, level)
	}
	for _, id := range raw.Basis {
		b.Basis = append(b.Basis, strings.TrimSpace(id))
	}
	if err := b.validate(); err != nil {
		return nil, fmt.Errorf("controls.report.brief: %w", err)
	}
	return b, nil
}

func (b *Brief) toYAML() *briefYAML {
	if b == nil {
		return nil
	}
	out := &briefYAML{Question: b.Question, Basis: append([]string(nil), b.Basis...)}
	for _, d := range b.Dimensions {
		dim := briefDimensionYAML{ID: d.ID, Label: d.Label, Question: d.Question, AllowUnknown: d.AllowUnknown}
		for _, v := range d.Values {
			dim.Values = append(dim.Values, briefValueYAML(v))
		}
		out.Dimensions = append(out.Dimensions, dim)
	}
	for _, l := range b.Levels {
		out.Levels = append(out.Levels, briefLevelYAML{
			Label: l.Label, Answer: l.Answer, Means: l.Means, Tone: l.Tone, When: cloneBriefConditions(l.When),
		})
	}
	return out
}

func (b *Brief) clone() *Brief {
	if b == nil {
		return nil
	}
	out := &Brief{Question: b.Question, Basis: append([]string(nil), b.Basis...)}
	for _, d := range b.Dimensions {
		d.Values = append([]BriefValue(nil), d.Values...)
		out.Dimensions = append(out.Dimensions, d)
	}
	for _, l := range b.Levels {
		l.When = cloneBriefConditions(l.When)
		out.Levels = append(out.Levels, l)
	}
	return out
}

func cloneBriefConditions(in []map[string][]string) []map[string][]string {
	if len(in) == 0 {
		return nil
	}
	out := make([]map[string][]string, 0, len(in))
	for _, cond := range in {
		c := make(map[string][]string, len(cond))
		for k, v := range cond {
			c[k] = append([]string(nil), v...)
		}
		out = append(out, c)
	}
	return out
}

var briefTones = map[string]bool{
	BriefToneCritical: true, BriefToneHigh: true, BriefToneMedium: true,
	BriefToneLow: true, BriefToneGood: true, BriefToneNeutral: true, "": true,
}

func (b *Brief) validate() error {
	if b.Question == "" {
		return fmt.Errorf("question required")
	}
	if len(b.Dimensions) == 0 {
		return fmt.Errorf("at least one dimension required")
	}
	dims := map[string]BriefDimension{}
	for i, d := range b.Dimensions {
		if d.ID == "" || d.Label == "" || d.Question == "" {
			return fmt.Errorf("dimensions[%d] requires id, label, and question", i)
		}
		if _, dup := dims[d.ID]; dup {
			return fmt.Errorf("dimension %q declared twice", d.ID)
		}
		if len(d.Values) < 2 {
			return fmt.Errorf("dimension %q needs at least two values", d.ID)
		}
		seen := map[string]bool{}
		for _, v := range d.Values {
			if v.ID == "" || v.Label == "" {
				return fmt.Errorf("dimension %q: every value requires id and label", d.ID)
			}
			if v.ID == BriefUnknown {
				return fmt.Errorf("dimension %q: %q is reserved; declare allow_unknown instead", d.ID, BriefUnknown)
			}
			if seen[v.ID] {
				return fmt.Errorf("dimension %q repeats value %q", d.ID, v.ID)
			}
			seen[v.ID] = true
		}
		dims[d.ID] = d
	}
	if len(b.Levels) < 2 {
		return fmt.Errorf("at least two levels required")
	}
	labels := map[string]bool{}
	for i, l := range b.Levels {
		if l.Label == "" || l.Means == "" {
			return fmt.Errorf("levels[%d] requires label and means", i)
		}
		if labels[l.Label] {
			return fmt.Errorf("level %q declared twice", l.Label)
		}
		labels[l.Label] = true
		if !briefTones[l.Tone] {
			return fmt.Errorf("level %q: unknown tone %q", l.Label, l.Tone)
		}
		last := i == len(b.Levels)-1
		if last && len(l.When) > 0 {
			return fmt.Errorf("level %q is the default and declares no conditions", l.Label)
		}
		if !last && len(l.When) == 0 {
			return fmt.Errorf("level %q requires conditions; only the last level is the default", l.Label)
		}
		for _, cond := range l.When {
			if len(cond) == 0 {
				return fmt.Errorf("level %q has an empty condition", l.Label)
			}
			for dim, values := range cond {
				d, ok := dims[dim]
				if !ok {
					return fmt.Errorf("level %q names undeclared dimension %q", l.Label, dim)
				}
				if len(values) == 0 {
					return fmt.Errorf("level %q lists no answers for %q", l.Label, dim)
				}
				for _, v := range values {
					if !d.declares(v) {
						return fmt.Errorf("level %q: dimension %q declares no answer %q", l.Label, dim, v)
					}
				}
			}
		}
	}
	for _, id := range b.Basis {
		if _, ok := dims[id]; !ok {
			return fmt.Errorf("basis names undeclared dimension %q", id)
		}
	}
	return nil
}

func (d BriefDimension) declares(value string) bool {
	for _, v := range d.Values {
		if v.ID == value {
			return true
		}
	}
	return false
}

// Value returns the declared answer, or false for BriefUnknown and undeclared ids.
func (d BriefDimension) Value(id string) (BriefValue, bool) {
	for _, v := range d.Values {
		if v.ID == id {
			return v, true
		}
	}
	return BriefValue{}, false
}

// Dimension returns a declared dimension by id.
func (b *Brief) Dimension(id string) (BriefDimension, bool) {
	if b == nil {
		return BriefDimension{}, false
	}
	for _, d := range b.Dimensions {
		if d.ID == id {
			return d, true
		}
	}
	return BriefDimension{}, false
}

// CheckAnswers reports the first way answers fail the declaration: a missing
// or undeclared dimension, or an answer the dimension does not declare.
func (b *Brief) CheckAnswers(answers map[string]string) error {
	if b == nil {
		return fmt.Errorf("this workflow declares no rating questions")
	}
	var undeclared []string
	for dim := range answers {
		if _, ok := b.Dimension(dim); !ok {
			undeclared = append(undeclared, dim)
		}
	}
	if len(undeclared) > 0 {
		sort.Strings(undeclared)
		return fmt.Errorf("undeclared rating question(s): %s", strings.Join(undeclared, ", "))
	}
	for _, d := range b.Dimensions {
		got := strings.TrimSpace(answers[d.ID])
		if got == "" {
			return fmt.Errorf("rating question %q is unanswered (want one of %s)", d.ID, d.answerList())
		}
		if got == BriefUnknown && d.AllowUnknown {
			continue
		}
		if !d.declares(got) {
			return fmt.Errorf("rating question %q has no answer %q (want one of %s)", d.ID, got, d.answerList())
		}
	}
	return nil
}

// Rateable returns answers the brief can rate: the answers themselves when
// they pass CheckAnswers, else every question unknown, so an unreadable rating
// spans every level instead of clearing the finding.
func (b *Brief) Rateable(answers map[string]string) map[string]string {
	if b.CheckAnswers(answers) == nil {
		return answers
	}
	out := make(map[string]string, len(b.Dimensions))
	for _, d := range b.Dimensions {
		out[d.ID] = BriefUnknown
	}
	return out
}

func (d BriefDimension) answerList() string {
	ids := make([]string, 0, len(d.Values)+1)
	for _, v := range d.Values {
		ids = append(ids, v.ID)
	}
	if d.AllowUnknown {
		ids = append(ids, BriefUnknown)
	}
	return strings.Join(ids, "|")
}

// DescribeAnswers writes the declared questions as the call shape an agent
// fills: each dimension with its allowed answers.
func (b *Brief) DescribeAnswers() string {
	if b == nil {
		return ""
	}
	parts := make([]string, 0, len(b.Dimensions))
	for _, d := range b.Dimensions {
		parts = append(parts, fmt.Sprintf("%s: %s", d.ID, d.answerList()))
	}
	return "{" + strings.Join(parts, ", ") + "}"
}

// DimensionIDs are the answer keys a rated finding carries, in declared order.
func (b *Brief) DimensionIDs() []string {
	if b == nil {
		return nil
	}
	out := make([]string, 0, len(b.Dimensions))
	for _, d := range b.Dimensions {
		out = append(out, d.ID)
	}
	return out
}

// PromptText lists the rating questions for an agent: each id, its question,
// and every answer it accepts with that answer's label. The lines are nested
// bullets, so they read as parts of the item that introduces them.
func (b *Brief) PromptText() string {
	if b == nil {
		return ""
	}
	var lines []string
	for _, d := range b.Dimensions {
		answers := make([]string, 0, len(d.Values)+1)
		for _, v := range d.Values {
			answers = append(answers, fmt.Sprintf("`%s` (%s)", v.ID, v.Label))
		}
		if d.AllowUnknown {
			answers = append(answers, "`"+BriefUnknown+"`")
		}
		lines = append(lines, fmt.Sprintf("  - `%s` — %s %s", d.ID, d.Question, strings.Join(answers, ", ")))
	}
	return strings.Join(lines, "\n")
}

// LevelRange rates one finding's answers. An unknown answer is tried as every
// declared value, so the range spans every level the unknown could decide.
func (b *Brief) LevelRange(answers map[string]string) (worst, best int) {
	worst, best = len(b.Levels)-1, 0
	for _, resolved := range b.resolutions(answers) {
		level := b.levelOf(resolved)
		worst = min(worst, level)
		best = max(best, level)
	}
	return worst, best
}

// Rate decides the level for a set of findings: the most severe level any of
// them could reach, and the most severe level they are certain to reach.
func (b *Brief) Rate(items []map[string]string) BriefRating {
	def := len(b.Levels) - 1
	out := BriefRating{Worst: def, Best: def, Decider: -1, Rated: len(items)}
	for i, answers := range items {
		worst, best := b.LevelRange(answers)
		if worst < out.Worst {
			out.Worst = worst
			out.Decider = i
		}
		out.Best = min(out.Best, best)
	}
	return out
}

// BasisPhrase says why the deciding finding set its level, from the phrases
// the basis dimensions declare for its answers.
func (b *Brief) BasisPhrase(answers map[string]string) string {
	var parts []string
	for _, id := range b.Basis {
		d, ok := b.Dimension(id)
		if !ok {
			continue
		}
		if v, ok := d.Value(strings.TrimSpace(answers[id])); ok && v.Phrase != "" {
			parts = append(parts, v.Phrase)
		}
	}
	return strings.Join(parts, "; ")
}

func (b *Brief) levelOf(answers map[string]string) int {
	for i, l := range b.Levels {
		for _, cond := range l.When {
			if conditionHolds(cond, answers) {
				return i
			}
		}
	}
	return len(b.Levels) - 1
}

func conditionHolds(cond map[string][]string, answers map[string]string) bool {
	for dim, allowed := range cond {
		if !slices.Contains(allowed, answers[dim]) {
			return false
		}
	}
	return true
}

// resolutions expands every unknown answer into each declared value.
func (b *Brief) resolutions(answers map[string]string) []map[string]string {
	out := []map[string]string{{}}
	for _, d := range b.Dimensions {
		got := strings.TrimSpace(answers[d.ID])
		choices := []string{got}
		if got == BriefUnknown {
			choices = choices[:0]
			for _, v := range d.Values {
				choices = append(choices, v.ID)
			}
		}
		next := make([]map[string]string, 0, len(out)*len(choices))
		for _, partial := range out {
			for _, c := range choices {
				m := make(map[string]string, len(partial)+1)
				for k, v := range partial {
					m[k] = v
				}
				m[d.ID] = c
				next = append(next, m)
			}
		}
		out = next
	}
	return out
}
