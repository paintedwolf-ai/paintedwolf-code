package turnload

import (
	"slices"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/lycaon/lycaon/internal/promptunit"
)

// Kind identifies a tool or instruction unit.
type Kind string

const (
	// KindTool is a loadable tool schema.
	KindTool Kind = "tool"
	// KindGuide is an instruction unit that carries its own decision.
	KindGuide Kind = "guide"
)

// Candidate is one unit the turn decision scores.
type Candidate struct {
	Kind Kind `json:"kind"`
	// ID is the tool name or the unit stem.
	ID string `json:"id"`
	// Description is what the engine reads about the unit.
	Description string `json:"description"`
	// NeededBy names the tools whose use makes a guide needed; a guide is
	// never omitted once the chat called or requested one of them.
	NeededBy []string `json:"needed_by,omitempty"`
}

// Needed reports whether the chat already called or requested a tool the
// candidate is needed by.
func (c Candidate) Needed(loaded []string) bool {
	for _, tool := range c.NeededBy {
		if slices.Contains(loaded, tool) {
			return true
		}
	}
	return false
}

// QuestionID is the engine question id for this candidate.
func (c Candidate) QuestionID() string { return string(c.Kind) + "." + c.ID }

// ToolCard is what the turn knows about one loadable tool.
type ToolCard struct {
	Name        string
	Description string
}

const (
	// CardMaxWords bounds a tool description the engine reads.
	CardMaxWords = 60
	// CardMaxRunes bounds the same text by length.
	CardMaxRunes = 480
)

// BoundDescription removes command-equivalence text and caps words and runes.
func BoundDescription(text string) string {
	text = strings.Join(strings.Fields(text), " ")
	if idx := strings.Index(text, " Replaces command:"); idx > 0 {
		text = text[:idx]
	}
	words := strings.Fields(text)
	if len(words) > CardMaxWords {
		words = words[:CardMaxWords]
		text = strings.Join(words, " ")
	}
	if utf8.RuneCountInString(text) > CardMaxRunes {
		runes := []rune(text)
		text = strings.TrimSpace(string(runes[:CardMaxRunes]))
	}
	return text
}

// OptionText bounds the description following an option's label.
func OptionText(description string, words int) string {
	fields := strings.Fields(BoundDescription(description))
	if words > 0 && len(fields) > words {
		fields = fields[:words]
	}
	return strings.TrimRight(strings.Join(fields, " "), ",;:")
}

// Option is the candidate's option text under spec.
func (c Candidate) Option(words int) string { return OptionText(c.Description, words) }

// ToolCandidates builds tool candidates from cards, sorted by name.
func ToolCandidates(cards []ToolCard) []Candidate {
	out := make([]Candidate, 0, len(cards))
	seen := make(map[string]bool, len(cards))
	for _, card := range cards {
		name := strings.TrimSpace(card.Name)
		if name == "" || seen[name] {
			continue
		}
		seen[name] = true
		out = append(out, Candidate{Kind: KindTool, ID: name, Description: BoundDescription(card.Description)})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// GuideCandidates builds guide candidates from the units a turn scores.
func GuideCandidates(units []promptunit.Unit) []Candidate {
	out := make([]Candidate, 0, len(units))
	for _, u := range units {
		out = append(out, Candidate{Kind: KindGuide, ID: u.ID, Description: u.Description, NeededBy: slices.Concat(u.Attaches, u.NeededWith)})
	}
	return out
}

// Candidates joins tool and guide candidates for one turn.
func Candidates(tools, guides []Candidate) []Candidate {
	out := make([]Candidate, 0, len(tools)+len(guides))
	out = append(out, tools...)
	return append(out, guides...)
}

// Candidates selects validated preload tools and pins their option descriptions.
func (s ToolsSpec) Candidates(candidates []Candidate) []Candidate {
	if s.Options == nil {
		return candidates
	}
	out := make([]Candidate, 0, len(candidates))
	for _, candidate := range candidates {
		if candidate.Kind == KindTool {
			text, known := s.Options[candidate.ID]
			if !known {
				continue
			}
			candidate.Description = text
		}
		out = append(out, candidate)
	}
	return out
}
