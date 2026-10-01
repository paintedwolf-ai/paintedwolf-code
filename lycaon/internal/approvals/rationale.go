package approvals

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/runeclamp"
	"github.com/lycaon/lycaon/pkg/api"
)

const (
	// MaxRationaleRunes bounds the one-sentence note the approval card shows.
	MaxRationaleRunes = 180

	maxPackSectionRunes   = 600
	maxRationaleArgsRunes = 400
)

// RationaleInputs holds the action context supplied to an approval explanation.
type RationaleInputs struct {
	UserIntent         string
	WorkerBrief        string
	ProgressSteps      []api.ProgressStep
	Tool               string
	Args               map[string]any
	Files              []string
	Project            string
	ExplanationWhat    string
	ExplanationWho     string
	ExplanationIfWrong string
	AssistantProse     string
	RecentResults      string
}

// FormatRationaleSystemPrompt renders the lite-model system prompt.
func FormatRationaleSystemPrompt(ctx context.Context) (string, error) {
	return guidance.RenderCatalog(ctx, guidance.UtilityRationaleSystemRef, nil)
}

// User intent precedes the held action and its supporting observations.
func FormatRationaleUserPrompt(ctx context.Context, in RationaleInputs) (string, error) {
	var steps strings.Builder
	for _, s := range in.ProgressSteps {
		label := strings.TrimSpace(s.Label)
		if label == "" {
			continue
		}
		state := strings.TrimSpace(s.State)
		if state == "" {
			state = "pending"
		}
		fmt.Fprintf(&steps, "- [%s] %s\n", state, label)
	}
	step := fmt.Sprintf("tool=%s", strings.TrimSpace(in.Tool))
	if args := truncateArgsJSON(in.Args); args != "" {
		step += "\nargs=" + args
	}
	if len(in.Files) > 0 {
		step += "\nfiles=" + strings.Join(in.Files, ", ")
	}
	if p := strings.TrimSpace(in.Project); p != "" {
		step += "\nproject=" + p
	}
	host := ""
	if in.ExplanationWhat != "" || in.ExplanationWho != "" || in.ExplanationIfWrong != "" {
		host = strings.TrimSpace(strings.Join([]string{
			in.ExplanationWhat,
			in.ExplanationWho,
			in.ExplanationIfWrong,
		}, "\n"))
	}
	return guidance.RenderCatalog(ctx, guidance.UtilityRationaleUserRef, map[string]any{
		"user_intent":    clampRunes(strings.TrimSpace(in.UserIntent), maxPackSectionRunes),
		"worker_brief":   clampRunes(strings.TrimSpace(in.WorkerBrief), maxPackSectionRunes),
		"recent_results": clampRunes(in.RecentResults, 1800),
		"tracked_plan":   clampRunes(strings.TrimSpace(steps.String()), maxPackSectionRunes),
		"gated_action":   clampRunes(step, maxPackSectionRunes),
		"host_facts":     clampRunes(host, maxPackSectionRunes),
		"assistant_note": clampRunes(strings.TrimSpace(in.AssistantProse), maxPackSectionRunes),
	})
}

// ClipRationale returns one bounded sentence.
func ClipRationale(s string) string {
	s = strings.Trim(strings.TrimSpace(s), `"'`)
	s = strings.Join(strings.Fields(s), " ")
	if s == "" {
		return ""
	}
	return clampRunes(firstSentence(s), MaxRationaleRunes)
}

// firstSentence preserves punctuation within tokens.
func firstSentence(s string) string {
	runes := []rune(s)
	for i, r := range runes {
		if !isSentenceTerminator(r) {
			continue
		}
		end := i
		for end+1 < len(runes) && isSentenceTerminator(runes[end+1]) {
			end++
		}
		next := end + 1
		if next >= len(runes) {
			return string(runes[:next])
		}
		if !unicode.IsSpace(runes[next]) {
			continue
		}
		for next < len(runes) && unicode.IsSpace(runes[next]) {
			next++
		}
		if next >= len(runes) || unicode.IsUpper(runes[next]) {
			return string(runes[:end+1])
		}
	}
	return s
}

func isSentenceTerminator(r rune) bool {
	return r == '.' || r == '!' || r == '?'
}

func truncateArgsJSON(args map[string]any) string {
	if len(args) == 0 {
		return ""
	}
	b, err := json.Marshal(args)
	if err != nil {
		return ""
	}
	s := string(b)
	if len(s) <= maxRationaleArgsRunes {
		return s
	}
	return runeclamp.Clamp(s, maxRationaleArgsRunes)
}

// Truncation prefers a word boundary within the rune limit.
func clampRunes(s string, max int) string {
	if max <= 0 || s == "" || utf8.RuneCountInString(s) <= max {
		return s
	}
	runes := []rune(s)[:max]
	for i := len(runes) - 1; i > max/2; i-- {
		if unicode.IsSpace(runes[i]) {
			runes = runes[:i]
			break
		}
	}
	return strings.TrimRightFunc(string(runes), unicode.IsSpace) + "…"
}
