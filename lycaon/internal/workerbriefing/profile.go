// Package workerbriefing summarizes one settled worker round result: what that
// round contributed, how that was verified, and what remains.
package workerbriefing

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"github.com/lycaon/lycaon/internal/briefing"
	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/observability"
	wire "github.com/lycaon/lycaon/pkg/api"
)

const (
	ProfileID      = "worker_result"
	profileVersion = "2"
	// sectionRunes bounds each source block; summaryMaxTokens bounds the answer.
	sectionRunes     = 2000
	summaryMaxTokens = 400
)

// summaryLabels are the summary's blocks, in display order.
var summaryLabels = []string{"Contribution", "Verification", "Remaining"}

var (
	summaryTemplate = briefing.Template{ID: "worker_result", Version: "2"}
	summaryContract = briefing.Contract{MaxRunes: 1800, Labels: summaryLabels}
)

// Workers resolves a result handle to the settled round job that produced it.
type Workers interface {
	LookupByResult(ctx context.Context, resultHandle string) (*wire.WorkerTask, bool)
}

type Profile struct{ workers Workers }

var _ briefing.Profile = (*Profile)(nil)

func New(workers Workers) *Profile { return &Profile{workers: workers} }

func (*Profile) ID() string              { return ProfileID }
func (*Profile) Version() string         { return profileVersion }
func (*Profile) Policy() briefing.Policy { return briefing.QueuedPolicy }

// Resolve reads the settled round by result handle.
func (p *Profile) Resolve(ctx context.Context, target briefing.Target) (briefing.Request, error) {
	var wanted wire.WorkerResultBriefingTarget
	decoder := json.NewDecoder(bytes.NewReader(target.Ref))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&wanted); err != nil {
		return briefing.Request{}, fmt.Errorf("%w: result_handle is required", briefing.ErrInvalidTarget)
	}
	handle := strings.TrimSpace(wanted.ResultHandle)
	if handle == "" {
		return briefing.Request{}, fmt.Errorf("%w: result_handle is required", briefing.ErrInvalidTarget)
	}
	if _, err := uuid.Parse(handle); err != nil {
		return briefing.Request{}, fmt.Errorf("%w: result_handle must be a uuid", briefing.ErrInvalidTarget)
	}
	task, ok := p.workers.LookupByResult(ctx, handle)
	if !ok || task == nil || task.ProjectID != target.ProjectID || task.CompletedAt == nil {
		return briefing.Request{}, fmt.Errorf("%w: unknown worker result", briefing.ErrTargetUnavailable)
	}
	section, err := summarySection(ctx, task, handle)
	if err != nil {
		return briefing.Request{}, err
	}
	canonical, err := json.Marshal(wire.WorkerResultBriefingTarget{ResultHandle: handle})
	if err != nil {
		return briefing.Request{}, err
	}
	return briefing.Request{
		ProjectID: target.ProjectID, ProjectDir: target.ProjectDir, ProfileID: ProfileID, Version: profileVersion,
		TargetKey: handle, TargetKind: "worker_result", TargetJSON: string(canonical), ScopeFingerprint: handle,
		Manual: target.Manual, Policy: p.Policy(), Sections: []briefing.SectionPlan{section},
	}, nil
}

// summarySection keeps the round's own report apart from host receipts, so the
// summary can tell claims from what was observed.
func summarySection(ctx context.Context, task *wire.WorkerTask, resultHandle string) (briefing.SectionPlan, error) {
	vars := map[string]any{"result_handle": resultHandle}
	if task.Round > 0 {
		vars["round"] = task.Round
	}
	if strings.TrimSpace(task.WorkerName) != "" {
		vars["worker"] = strings.TrimSpace(task.WorkerName)
	}
	var items []briefing.Item
	add := func(key string, role briefing.Role, kind, text string) {
		text = observability.RedactCaptureText(strings.TrimSpace(text))
		item := briefing.Item{Key: key, Role: role, SourceKind: kind, Ref: "worker_result:" + resultHandle, Audience: task.ParentSessionID}
		switch runes := []rune(text); {
		case text == "":
			item.Inclusion, item.Reason = briefing.Unavailable, "absent"
		case len(runes) > sectionRunes:
			text = string(runes[:sectionRunes])
			item.Inclusion, item.Text = briefing.IncludedTruncated, text
			vars[key] = text
		default:
			item.Inclusion, item.Text = briefing.IncludedWhole, text
			vars[key] = text
		}
		items = append(items, item)
	}
	add("assignment", briefing.RoleGoal, "assignment", task.Brief)
	add("outcome", briefing.RoleObservation, "worker_status", outcome(task))
	add("report", briefing.RoleContext, "worker_report", report(task))
	add("receipts", briefing.RoleVerification, "worker_receipts", receipts(task))
	system, err := guidance.RenderCatalog(ctx, guidance.UtilityWorkerResultSystemRef, nil)
	if err != nil {
		return briefing.SectionPlan{}, err
	}
	user, err := guidance.RenderCatalog(ctx, guidance.UtilityWorkerResultUserRef, vars)
	if err != nil {
		return briefing.SectionPlan{}, err
	}
	manifest := briefing.Manifest{ProfileID: ProfileID, ProfileVersion: profileVersion, SectionID: "summary", Template: summaryTemplate, Contract: summaryContract, Items: items}.Sealed()
	return briefing.SectionPlan{ID: "summary", Mode: briefing.Generated, Manifest: manifest,
		Prompt: briefing.Prompt{System: system, User: user, MaxTokens: summaryMaxTokens}}, nil
}

func outcome(task *wire.WorkerTask) string {
	text := "status: " + string(task.Status)
	if task.ResultDisposition != "" {
		text += "\ndisposition: " + string(task.ResultDisposition)
	}
	if task.ShelveReason != "" {
		text += "\nshelve_reason: " + task.ShelveReason
	}
	if task.Failure != nil {
		text += "\nfailure: " + task.Failure.Code + " " + task.Failure.Message
	} else if task.Error != "" {
		text += "\nerror: " + task.Error
	}
	return text
}

// report is what the worker said about its own work.
func report(task *wire.WorkerTask) string {
	if task.Result == nil {
		return ""
	}
	parts := []string{task.Result.Summary}
	if report := task.Result.CompletionReport; report != nil {
		parts = append(parts, list("objectives met", report.ObjectivesMet), list("remaining risk", report.RemainingRisk))
		if report.SuggestedNextTask != "" {
			parts = append(parts, "suggested next task: "+report.SuggestedNextTask)
		}
	}
	return strings.Join(nonEmpty(parts), "\n")
}

// receipts are what the host observed of the work.
func receipts(task *wire.WorkerTask) string {
	var parts []string
	if task.Result != nil && task.Result.ChangeReport != nil {
		change := task.Result.ChangeReport
		parts = append(parts, list("changed paths", change.ChangedPaths), fmt.Sprintf("tool receipts: %d", change.ReceiptCount))
		for _, stat := range change.DiffStat {
			parts = append(parts, fmt.Sprintf("%s +%d -%d", stat.Path, stat.Insertions, stat.Deletions))
		}
	}
	if task.Result != nil && task.Result.CompletionReport != nil && task.Result.CompletionReport.Verification != nil {
		raw, err := json.Marshal(task.Result.CompletionReport.Verification)
		if err == nil {
			parts = append(parts, "verification: "+string(raw))
		}
	}
	return strings.Join(nonEmpty(parts), "\n")
}

func list(label string, values []string) string {
	if len(values) == 0 {
		return ""
	}
	return label + ": " + strings.Join(values, "; ")
}

func nonEmpty(values []string) []string {
	out := values[:0]
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			out = append(out, value)
		}
	}
	return out
}
