package stepbriefing

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/lycaon/lycaon/internal/briefing"
	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/observability"
	"github.com/lycaon/lycaon/internal/sourceledger"
	"github.com/lycaon/lycaon/internal/tools/native/sourceview"
	wire "github.com/lycaon/lycaon/pkg/api"
)

const (
	// diffFiles, diffRunes and diffTotal bound the changes one step reads.
	diffFiles       = 8
	diffRunes       = 1500
	diffTotal       = 6000
	diffCells       = 1 << 20
	diffContext     = 2
	changeMaxTokens = 80
)

var changeTemplate = briefing.Template{ID: "step_change", Version: "1"}

func (p *Profile) sections(ctx context.Context, projectID string, s step) ([]briefing.SectionPlan, error) {
	var sections []briefing.SectionPlan
	if s.session != "" && s.call != "" {
		purpose, err := p.purposeSection(ctx, s)
		if err != nil {
			return nil, err
		}
		sections = append(sections, purpose)
	}
	change, ok, err := p.changeSection(ctx, projectID, s)
	if err != nil {
		return nil, err
	}
	if ok {
		sections = append(sections, change)
	}
	return append(sections, outcomeSection(s)), nil
}

func (p *Profile) purposeSection(ctx context.Context, s step) (briefing.SectionPlan, error) {
	read, err := p.cause.ReadActionCause(ctx, briefing.ActionRef{SessionID: s.session, ToolCallID: s.call})
	if err != nil {
		return briefing.SectionPlan{}, err
	}
	action := briefing.ActionEvidence{Action: briefing.ActionFacts{
		Tool: read.Call.Name, CallRef: "tool_call:" + s.call, Args: callArgs(read.Call.Args), Session: s.session,
	}, PhaseScope: phaseScope(s)}
	briefing.ApplyCause(&action, read)
	return briefing.PurposeSection(ctx, action)
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

// phaseScope names the part of an invocation a step covers: a Git movement
// inside a command, or one file of a call that changed several.
func phaseScope(s step) string {
	switch {
	case s.git != nil:
		return fmt.Sprintf("git %s: %s → %s", s.git.Kind, firstNonEmpty(s.git.FromRef, s.git.FromCommit), firstNonEmpty(s.git.ToRef, s.git.ToCommit))
	case s.target.Kind == "effect" && s.effects[0].BatchID != "":
		return "file: " + s.effects[0].Path
	default:
		return ""
	}
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}

// changeSection asks what the step changed when its diffs, movement or
// command add material the counts do not show.
func (p *Profile) changeSection(ctx context.Context, projectID string, s step) (briefing.SectionPlan, bool, error) {
	diffs, cut, err := p.diffs(ctx, projectID, s.effects)
	if err != nil {
		return briefing.SectionPlan{}, false, err
	}
	vars := map[string]any{}
	var items []briefing.Item
	add := func(key string, role briefing.Role, kind, ref, text string, truncated bool) {
		text = observability.RedactCaptureText(strings.TrimSpace(text))
		item := briefing.Item{Key: key, Role: role, SourceKind: kind, Ref: ref, Audience: s.session}
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
	add("diffs", briefing.RoleObservation, "source_diff", s.target.TargetKey, diffs, cut)
	if s.git != nil {
		add("movement", briefing.RoleObservation, "git_movement", s.git.ID, fmt.Sprintf("%s from %s to %s\n%s", s.git.Kind,
			firstNonEmpty(s.git.FromRef, s.git.FromCommit), firstNonEmpty(s.git.ToRef, s.git.ToCommit), s.git.Detail), false)
	}
	if s.window != nil {
		add("command", briefing.RoleAction, "command_window", s.window.ID, s.window.CommandLine+"\nstate: "+string(s.window.State), false)
	}
	if len(vars) == 0 {
		return briefing.SectionPlan{}, false, nil
	}
	system, err := guidance.RenderCatalog(ctx, guidance.UtilityStepChangeSystemRef, nil)
	if err != nil {
		return briefing.SectionPlan{}, false, err
	}
	user, err := guidance.RenderCatalog(ctx, guidance.UtilityStepChangeUserRef, vars)
	if err != nil {
		return briefing.SectionPlan{}, false, err
	}
	manifest := briefing.Manifest{ProfileID: ProfileID, ProfileVersion: profileVersion, SectionID: "change", Template: changeTemplate, Contract: briefing.OneSentence, Items: items}.Sealed()
	return briefing.SectionPlan{ID: "change", Mode: briefing.Generated, Manifest: manifest,
		Prompt: briefing.Prompt{System: system, User: user, MaxTokens: changeMaxTokens}}, true, nil
}

// diffs renders bounded unified diffs of the step's files; binary, missing
// or oversized sides are named but not shown.
func (p *Profile) diffs(ctx context.Context, projectID string, effects []sourceledger.Effect) (string, bool, error) {
	var b strings.Builder
	cut := len(effects) > diffFiles
	for _, effect := range effects[:min(len(effects), diffFiles)] {
		comparison, err := p.ledger.CompareEffect(ctx, projectID, effect.ID)
		if err != nil {
			return "", false, err
		}
		text := fmt.Sprintf("%s %s: content unavailable\n", effect.Op, effect.Path)
		if readable(comparison.Before) && readable(comparison.After) {
			diff, err := sourceview.UnifiedDiffWithin(comparison.Before.Path, comparison.After.Path, comparison.Before.Content, comparison.After.Content, diffContext, diffCells)
			if err == nil {
				text = diff
			}
		}
		if runes := []rune(text); len(runes) > diffRunes {
			text, cut = string(runes[:diffRunes])+"\n", true
		}
		if b.Len()+len(text) > diffTotal {
			return b.String(), true, nil
		}
		b.WriteString(text)
	}
	return b.String(), cut, nil
}

func readable(side sourceledger.ComparisonSide) bool {
	return side.Availability == sourceledger.ContentAvailable || side.Availability == sourceledger.ContentAbsent
}

func outcomeSection(s step) briefing.SectionPlan {
	text := outcomeText(s)
	items := []briefing.Item{{Key: "effects", Role: briefing.RoleObservation, SourceKind: "source_effects", Ref: s.target.TargetKey,
		Inclusion: briefing.IncludedWhole, Text: text, Audience: s.session}}
	manifest := briefing.Manifest{ProfileID: ProfileID, ProfileVersion: profileVersion, SectionID: "outcome",
		Template: briefing.Template{ID: "step_outcome", Version: "1"}, Contract: briefing.OneSentence, Items: items}.Sealed()
	return briefing.SectionPlan{ID: "outcome", Mode: briefing.Deterministic, Manifest: manifest, Text: text}
}

// outcomeText counts what the step did to files, and how its command ended.
func outcomeText(s step) string {
	files := map[string]bool{}
	for _, effect := range s.effects {
		files[effect.FileID] = true
	}
	noun := "files"
	if len(files) == 1 {
		noun = "file"
	}
	text := fmt.Sprintf("Changed %d %s", len(files), noun)
	if s.window != nil && s.window.State == wire.SourceCommandWindowInterrupted {
		text += "; the command was interrupted"
	}
	return text
}
