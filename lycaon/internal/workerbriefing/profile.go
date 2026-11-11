// Package workerbriefing summarizes a finished worker: what it contributed,
// how that was verified, and what remains.
package workerbriefing

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/lycaon/lycaon/internal/briefing"
	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/observability"
	wire "github.com/lycaon/lycaon/pkg/api"
)

const (
	ProfileID      = "worker_result"
	profileVersion = "1"
	// sectionRunes bounds each source block; summaryMaxTokens bounds the answer.
	sectionRunes     = 2000
	summaryMaxTokens = 400
)

// Labels are the summary's blocks, in display order.
var Labels = []string{"Contribution", "Verification", "Remaining"}

var (
	summaryTemplate = briefing.Template{ID: "worker_result", Version: "1"}
	summaryContract = briefing.Contract{MaxRunes: 1800, Labels: Labels}
)

// Workers reads a worker job.
type Workers interface {
	Get(jobID string) (*wire.WorkerTask, bool)
}

type Profile struct{ workers Workers }

var _ briefing.Profile = (*Profile)(nil)

func New(workers Workers) *Profile { return &Profile{workers: workers} }

func (*Profile) ID() string              { return ProfileID }
func (*Profile) Version() string         { return profileVersion }
func (*Profile) Policy() briefing.Policy { return briefing.QueuedPolicy }

// Resolve reads the worker by id and checks it finished at the revision
// the person is looking at.
func (p *Profile) Resolve(ctx context.Context, target briefing.Target) (briefing.Request, error) {
	var wanted wire.WorkerResultBriefingTarget
	decoder := json.NewDecoder(bytes.NewReader(target.Ref))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&wanted); err != nil || strings.TrimSpace(wanted.JobID) == "" || strings.TrimSpace(wanted.ResultRevision) == "" {
		return briefing.Request{}, fmt.Errorf("%w: job_id and result_revision are required", briefing.ErrInvalidTarget)
	}
	task, ok := p.workers.Get(wanted.JobID)
	if !ok || task == nil || task.ProjectID != target.ProjectID {
		return briefing.Request{}, fmt.Errorf("%w: unknown worker", briefing.ErrTargetUnavailable)
	}
	revision, err := time.Parse(time.RFC3339Nano, wanted.ResultRevision)
	if err != nil {
		return briefing.Request{}, fmt.Errorf("%w: result_revision is a completion time", briefing.ErrInvalidTarget)
	}
	if task.CompletedAt == nil || !task.CompletedAt.Equal(revision) {
		return briefing.Request{}, fmt.Errorf("%w: the worker has not finished at that revision", briefing.ErrTargetUnavailable)
	}
	section, err := summarySection(ctx, task)
	if err != nil {
		return briefing.Request{}, err
	}
	canonical, err := json.Marshal(wanted)
	if err != nil {
		return briefing.Request{}, err
	}
	key := wanted.JobID + "@" + resultRevision(task)
	return briefing.Request{
		ProjectID: target.ProjectID, ProjectDir: target.ProjectDir, ProfileID: ProfileID, Version: profileVersion,
		TargetKey: key, TargetKind: "worker", TargetJSON: string(canonical), ScopeFingerprint: key,
		Manual: target.Manual, Policy: p.Policy(), Sections: []briefing.SectionPlan{section},
	}, nil
}

// resultRevision identifies a worker's settled result.
func resultRevision(task *wire.WorkerTask) string {
	return task.CompletedAt.UTC().Format(time.RFC3339Nano)
}

// summarySection keeps the worker's own report apart from host receipts, so
// the summary can tell claims from what was observed.
func summarySection(ctx context.Context, task *wire.WorkerTask) (briefing.SectionPlan, error) {
	vars := map[string]any{}
	var items []briefing.Item
	add := func(key string, role briefing.Role, kind, text string) {
		text = observability.RedactCaptureText(strings.TrimSpace(text))
		item := briefing.Item{Key: key, Role: role, SourceKind: kind, Ref: "worker_job:" + task.ID, Audience: task.ParentSessionID}
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
