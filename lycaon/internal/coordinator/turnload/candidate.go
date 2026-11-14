package turnload

import (
	"sort"
	"strings"
	"unicode/utf8"
)

// Candidate is one loadable tool the turn decision scores.
type Candidate struct {
	// ID is the tool name.
	ID string `json:"id"`
	// Description is what the engine reads about the tool.
	Description string `json:"description"`
}

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
		out = append(out, Candidate{ID: name, Description: BoundDescription(card.Description)})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// Candidates selects validated preload tools and pins their option descriptions.
func (s ToolsSpec) Candidates(candidates []Candidate) []Candidate {
	if s.Options == nil {
		return candidates
	}
	out := make([]Candidate, 0, len(candidates))
	for _, candidate := range candidates {
		text, known := s.Options[candidate.ID]
		if !known {
			continue
		}
		candidate.Description = text
		out = append(out, candidate)
	}
	return out
}
