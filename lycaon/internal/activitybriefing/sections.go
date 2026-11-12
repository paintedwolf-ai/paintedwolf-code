package activitybriefing

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/lycaon/lycaon/internal/briefing"
	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/observability"
	wire "github.com/lycaon/lycaon/pkg/api"
)

const (
	// receiptLimit and outputRunes bound what one change section reads.
	receiptLimit    = 16
	outputRunes     = 1024
	outputTotal     = 4096
	changeMaxTokens = 120
)

var changeTemplate = briefing.Template{ID: "activity_change", Version: "1"}

func buildSections(ctx context.Context, ev evidence) ([]briefing.SectionPlan, error) {
	choice := decide(ev.facts)
	purpose, err := purposeSection(ctx, ev, choice)
	if err != nil {
		return nil, err
	}
	sections := []briefing.SectionPlan{purpose}
	if choice.change {
		change, err := changeSection(ctx, ev)
		if err != nil {
			return nil, err
		}
		sections = append(sections, change)
	}
	return append(sections, outcomeSection(ev, choice)), nil
}

func purposeSection(ctx context.Context, ev evidence, choice decision) (briefing.SectionPlan, error) {
	call := ev.cause.Call
	action := briefing.ActionEvidence{Action: briefing.ActionFacts{
		Tool: call.Name, CallRef: "tool_call:" + ev.target.ToolCallIds[0], Args: callArgs(call.Args), Session: ev.target.SessionID,
	}, PhaseScope: phaseScope(ev)}
	briefing.ApplyCause(&action, ev.cause)
	section, err := briefing.PurposeSection(ctx, action)
	section.Mode = choice.purpose
	return section, err
}

func callArgs(raw string) map[string]any {
	if raw == "" {
		return nil
	}
	var args map[string]any
	if err := json.Unmarshal([]byte(raw), &args); err != nil {
		return map[string]any{"arguments": raw}
	}
	return args
}

// phaseScope names the steps a card covers when it is part of its program or
// spans several calls; a card that is its whole invocation has none.
func phaseScope(ev evidence) string {
	var steps []string
	for _, activity := range ev.facts.activities {
		for _, step := range activity.Steps {
			steps = append(steps, step.Command)
		}
	}
	whole := !ev.partial && len(ev.target.ToolCallIds) <= 1
	if whole || len(steps) == 0 {
		return ""
	}
	return "steps: " + strings.Join(steps, "; ")
}

func changeSection(ctx context.Context, ev evidence) (briefing.SectionPlan, error) {
	vars := map[string]any{}
	var items []briefing.Item
	add := func(key string, role briefing.Role, kind, ref, text string, truncated bool) {
		text = observability.RedactCaptureText(strings.TrimSpace(text))
		item := briefing.Item{Key: key, Role: role, SourceKind: kind, Ref: ref, Audience: ev.target.SessionID}
		switch {
		case text == "":
			item.Inclusion, item.Reason = briefing.Unavailable, "absent"
		case truncated:
			item.Inclusion, item.Text = briefing.IncludedTruncated, text
			vars[key] = text
		default:
			item.Inclusion, item.Text = briefing.IncludedWhole, text
			vars[key] = text
		}
		items = append(items, item)
	}
	add("steps", briefing.RoleStructure, "activity_steps", "", formatSteps(ev.facts.activities), false)
	receipts, receiptsCut := formatReceipts(ev.executions)
	add("receipts", briefing.RoleObservation, "command_receipts", programRefs(ev.target), receipts, receiptsCut)
	output, outputCut := formatOutput(ev.executions)
	add("output", briefing.RoleObservation, "command_output", "", output, outputCut)
	if result := ev.cause.Call.Result; result != nil {
		add("call_result", briefing.RoleObservation, "tool_result", result.Ref, result.Outcome+": "+result.Content, len(result.Content) >= briefing.ResultReadChars)
	}
	system, err := guidance.RenderCatalog(ctx, guidance.UtilityActivityChangeSystemRef, nil)
	if err != nil {
		return briefing.SectionPlan{}, err
	}
	user, err := guidance.RenderCatalog(ctx, guidance.UtilityActivityChangeUserRef, vars)
	if err != nil {
		return briefing.SectionPlan{}, err
	}
	manifest := briefing.Manifest{ProfileID: ProfileID, ProfileVersion: profileVersion, SectionID: "change", Template: changeTemplate, Contract: briefing.TwoSentences, Items: items}.Sealed()
	return briefing.SectionPlan{ID: "change", Mode: briefing.Generated, Manifest: manifest, Prompt: briefing.Prompt{System: system, User: user, MaxTokens: changeMaxTokens}}, nil
}

func programRefs(target wire.ToolActivityBriefingTarget) string {
	refs := make([]string, 0, len(target.Members))
	for _, member := range target.Members {
		refs = append(refs, member.ProgramID+"/"+member.ActivityID)
	}
	return strings.Join(refs, ",")
}

func formatSteps(activities []wire.CommandActivity) string {
	var b strings.Builder
	for _, activity := range activities {
		for _, step := range activity.Steps {
			runs := "1 run"
			if step.Count != 1 {
				runs = fmt.Sprintf("%d runs", step.Count)
			}
			fmt.Fprintf(&b, "- %s: %s, %s\n", step.Command, step.State, runs)
		}
	}
	return b.String()
}

// receiptFacts are the receipt fields a change explanation may use.
type receiptFacts struct {
	Command           string `json:"command"`
	State             string `json:"state"`
	StatusCode        *int   `json:"status_code,omitempty"`
	StopCode          string `json:"stop_code,omitempty"`
	HostStopCode      string `json:"host_stop_code,omitempty"`
	EffectCertainty   string `json:"effect_certainty,omitempty"`
	AcceptedRevision  string `json:"accepted_revision,omitempty"`
	PublishedRevision string `json:"published_revision,omitempty"`
	RevisionConflict  bool   `json:"revision_conflict,omitempty"`
}

func formatReceipts(executions []wire.CommandExecutionReceipt) (string, bool) {
	var lines []string
	for _, execution := range executions[:min(len(executions), receiptLimit)] {
		raw, err := json.Marshal(receiptFacts{
			Command: execution.Command, State: execution.State, StatusCode: execution.StatusCode, StopCode: execution.StopCode,
			HostStopCode: execution.HostStopCode, EffectCertainty: execution.EffectCertainty, AcceptedRevision: execution.AcceptedRevision,
			PublishedRevision: execution.PublishedRevision, RevisionConflict: execution.RevisionConflict != nil,
		})
		if err == nil {
			lines = append(lines, string(raw))
		}
	}
	return strings.Join(lines, "\n"), len(executions) > receiptLimit
}

// formatOutput keeps each stream's last lines, which hold a settled
// command's result, within a per-stream and total bound.
func formatOutput(executions []wire.CommandExecutionReceipt) (string, bool) {
	var b strings.Builder
	cut := false
	for _, execution := range executions {
		for _, output := range execution.Outputs {
			text := output.TailPreview
			if text == "" {
				text = output.Preview
			}
			if text == "" {
				continue
			}
			runes := []rune(text)
			if len(runes) > outputRunes {
				text, cut = string(runes[len(runes)-outputRunes:]), true
			}
			cut = cut || output.PreviewTruncated
			entry := fmt.Sprintf("[%s %s]\n%s\n", execution.Command, output.Stream, text)
			if b.Len()+len(entry) > outputTotal {
				return b.String(), true
			}
			b.WriteString(entry)
		}
	}
	return b.String(), cut
}

func outcomeSection(ev evidence, choice decision) briefing.SectionPlan {
	text := outcomeText(ev)
	items := []briefing.Item{
		{Key: "policy", Role: briefing.RoleContext, SourceKind: "generation_policy", Inclusion: briefing.IncludedWhole, Text: string(choice.row)},
		{Key: "state", Role: briefing.RoleObservation, SourceKind: "activity_state", Inclusion: briefing.IncludedWhole, Text: text, Audience: ev.target.SessionID},
	}
	manifest := briefing.Manifest{ProfileID: ProfileID, ProfileVersion: profileVersion, SectionID: "outcome", Template: briefing.Template{ID: "activity_outcome", Version: "1"},
		Contract: briefing.OneSentence, Items: items}.Sealed()
	return briefing.SectionPlan{ID: "outcome", Mode: briefing.Deterministic, Manifest: manifest, Text: text}
}

// outcomeText states each activity's settled state and exit status.
func outcomeText(ev evidence) string {
	if !ev.facts.command {
		if result := ev.cause.Call.Result; result != nil && result.Outcome != "" {
			return "Result: " + result.Outcome
		}
		return "Running"
	}
	parts := make([]string, 0, len(ev.facts.activities))
	for _, activity := range ev.facts.activities {
		part := strings.ToUpper(activity.State[:min(1, len(activity.State))]) + activity.State[min(1, len(activity.State)):]
		if activity.StatusCode != nil {
			part += fmt.Sprintf(", exit %d", *activity.StatusCode)
		}
		parts = append(parts, part)
	}
	return strings.Join(parts, "; ")
}
