package briefing

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/observability"
	"github.com/lycaon/lycaon/internal/runeclamp"
)

// PurposeSectionID is the section every action profile shares with approvals.
const PurposeSectionID = "purpose"

// purposeTemplate names the action_purpose question; bump Version whenever
// either rationale template changes. The manifest names the question, not the
// asking profile, so an approval and a later step asking it of the same
// material share one result.
var purposeTemplate = Template{ID: "action_purpose", Version: "2"}

// Limits match what the approval card has always submitted.
const (
	sectionRunes     = 600
	resultRunes      = 450
	resultsRunes     = 1800
	argsRunes        = 400
	recentResultsMax = 3
	purposeMaxTokens = 120
)

// Excerpt is one optional text input with its source.
type Excerpt struct {
	Text string
	Ref  string
	// Unavailable explains why an input that applies has no readable form.
	Unavailable string
}

// ResultExcerpt is an earlier tool result observed before the action.
type ResultExcerpt struct {
	Tool    string `json:"tool"`
	Outcome string `json:"outcome"`
	Content string `json:"content"`
	Ref     string `json:"-"`
}

// PlanStep is one tracked-plan item.
type PlanStep struct {
	Label string
	State string
}

// Reasoning is a bounded, redacted suffix of the model's plaintext reasoning
// from the output that contains the action.
type Reasoning struct {
	Text      string
	Ref       string
	Shared    bool
	Truncated bool
}

// ActionFacts identify the action being explained.
type ActionFacts struct {
	Tool    string
	CallRef string
	Args    map[string]any
	Files   []string
	Project string
}

// PlanInput is the tracked plan at the action, or why it is unavailable.
type PlanInput struct {
	Steps []PlanStep
	Gap   string
}

// ReasoningInput is the reasoning excerpt, or why it is unavailable.
type ReasoningInput struct {
	Excerpt *Reasoning
	Gap     string
}

// ActionEvidence is what the action_purpose question reads. Live approvals
// and historical steps fill it from different sources.
type ActionEvidence struct {
	Action ActionFacts
	// HostFacts are the approval request's consequence facts.
	HostFacts      Excerpt
	UserIntent     Excerpt
	WorkerBrief    Excerpt
	AssistantProse Excerpt
	Reasoning      ReasoningInput
	Results        []ResultExcerpt
	Plan           PlanInput
	// PhaseScope narrows a target below its invocation, such as one phase of
	// a multi-phase command; empty when the target is the whole invocation.
	PhaseScope string
}

// PurposeSection builds the shared purpose section. Every input is redacted,
// then clamped exactly as the approval card clamps it.
func PurposeSection(ctx context.Context, ev ActionEvidence) (SectionPlan, error) {
	vars := map[string]any{}
	var items []Item
	add := func(key string, role Role, kind, ref, text string, limit int, gap string) {
		text = observability.RedactCaptureText(strings.TrimSpace(text))
		item := Item{Key: key, Role: role, SourceKind: kind, Ref: ref}
		switch {
		case text != "":
			clamped := clampSection(text, limit)
			item.Text, item.Inclusion = clamped, IncludedWhole
			if clamped != text {
				item.Inclusion = IncludedTruncated
			}
			vars[key] = clamped
		case gap != "":
			item.Inclusion, item.Reason = Unavailable, gap
		default:
			item.Inclusion, item.Reason = Unavailable, "absent"
		}
		items = append(items, item)
	}
	add("user_intent", RoleGoal, "message", ev.UserIntent.Ref, ev.UserIntent.Text, sectionRunes, ev.UserIntent.Unavailable)
	add("worker_brief", RoleGoal, "assignment", ev.WorkerBrief.Ref, ev.WorkerBrief.Text, sectionRunes, ev.WorkerBrief.Unavailable)
	add("tracked_plan", RoleContext, "progress", "", formatPlan(ev.Plan.Steps), sectionRunes, ev.Plan.Gap)
	add("recent_results", RoleObservation, "tool_result", resultRefs(ev.Results), formatResults(ev.Results), resultsRunes, "")
	add("action", RoleAction, "tool_call", ev.Action.CallRef, formatAction(ev.Action), sectionRunes, "")
	add("host_facts", RoleContext, "approval_request", "", ev.HostFacts.Text, sectionRunes, ev.HostFacts.Unavailable)
	add("assistant_note", RoleContext, "message", ev.AssistantProse.Ref, ev.AssistantProse.Text, sectionRunes, ev.AssistantProse.Unavailable)
	reasoning, reasoningRef := "", ""
	if excerpt := ev.Reasoning.Excerpt; excerpt != nil {
		reasoning, reasoningRef = excerpt.Text, excerpt.Ref
	}
	add("assistant_reasoning", RoleContext, "reasoning", reasoningRef, reasoning, sectionRunes, ev.Reasoning.Gap)
	if excerpt := ev.Reasoning.Excerpt; excerpt != nil && (excerpt.Shared || excerpt.Truncated) {
		items[len(items)-1].Reason = strings.Join(nonEmpty(flag(excerpt.Shared, "shared across calls"), flag(excerpt.Truncated, "suffix")), ", ")
	}
	if strings.TrimSpace(ev.PhaseScope) != "" {
		add("phase_scope", RoleAction, "activity", "", ev.PhaseScope, sectionRunes, "")
	}
	system, err := guidance.RenderCatalog(ctx, guidance.UtilityRationaleSystemRef, nil)
	if err != nil {
		return SectionPlan{}, err
	}
	user, err := guidance.RenderCatalog(ctx, guidance.UtilityRationaleUserRef, vars)
	if err != nil {
		return SectionPlan{}, err
	}
	manifest := Manifest{ProfileID: purposeTemplate.ID, ProfileVersion: purposeTemplate.Version, SectionID: PurposeSectionID, Template: purposeTemplate, Contract: OneSentence, Items: items}.Sealed()
	return SectionPlan{ID: PurposeSectionID, Mode: Generated, Manifest: manifest, Prompt: Prompt{System: system, User: user, MaxTokens: purposeMaxTokens}}, nil
}

func formatPlan(steps []PlanStep) string {
	var b strings.Builder
	for _, step := range steps {
		label := strings.TrimSpace(step.Label)
		if label == "" {
			continue
		}
		state := strings.TrimSpace(step.State)
		if state == "" {
			state = "pending"
		}
		fmt.Fprintf(&b, "- [%s] %s\n", state, label)
	}
	return b.String()
}

func formatResults(results []ResultExcerpt) string {
	if len(results) == 0 {
		return ""
	}
	results = results[max(0, len(results)-recentResultsMax):]
	clamped := make([]ResultExcerpt, len(results))
	for i, result := range results {
		result.Content = runeclamp.Clamp(observability.RedactCaptureText(result.Content), resultRunes)
		clamped[i] = result
	}
	data, err := json.Marshal(clamped)
	if err != nil {
		return ""
	}
	return string(data)
}

func resultRefs(results []ResultExcerpt) string {
	refs := make([]string, 0, len(results))
	for _, result := range results[max(0, len(results)-recentResultsMax):] {
		if result.Ref != "" {
			refs = append(refs, result.Ref)
		}
	}
	return strings.Join(refs, ",")
}

func formatAction(action ActionFacts) string {
	step := fmt.Sprintf("tool=%s", strings.TrimSpace(action.Tool))
	if len(action.Args) > 0 {
		if data, err := json.Marshal(observability.RedactCaptureValue(action.Args)); err == nil {
			step += "\nargs=" + runeclamp.Clamp(string(data), argsRunes)
		}
	}
	if len(action.Files) > 0 {
		files := make([]string, len(action.Files))
		for i, file := range action.Files {
			files[i] = observability.RedactCaptureText(file)
		}
		step += "\nfiles=" + strings.Join(files, ", ")
	}
	if project := strings.TrimSpace(action.Project); project != "" {
		step += "\nproject=" + project
	}
	return step
}

func flag(on bool, label string) string {
	if on {
		return label
	}
	return ""
}

func nonEmpty(values ...string) []string {
	out := values[:0]
	for _, value := range values {
		if value != "" {
			out = append(out, value)
		}
	}
	return out
}

// clampSection cuts at a word boundary and marks the cut, as approval inputs
// have always been clamped.
func clampSection(s string, max int) string {
	if max <= 0 || utf8.RuneCountInString(s) <= max {
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
