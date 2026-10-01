package agentpresence

import (
	"context"
	"encoding/json"
	"slices"
	"strings"

	"github.com/lycaon/lycaon/pkg/api"
)

// Drafts reads a completed worker job's pending changes to primary files.
type Drafts interface {
	// ReadyFiles returns each changed file with its changed lines in the current primary text.
	ReadyFiles(ctx context.Context, jobID string) ([]DraftFile, error)
}

// SetDrafts installs the worker draft reader once workers exist.
func (t *Tracker) SetDrafts(drafts Drafts) {
	t.enqueue(func() { t.drafts = drafts })
}

// WorkerReserved records paths a worker session reserved before drafting them.
func (t *Tracker) WorkerReserved(ctx context.Context, sessionID string, targets []Target) {
	if t == nil || len(targets) == 0 {
		return
	}
	ctx = context.WithoutCancel(ctx)
	t.enqueue(func() {
		ref, ok := t.chat(ctx, sessionID)
		if !ok || ref.JobID == "" {
			return
		}
		files := make([]DraftFile, len(targets))
		for i, target := range targets {
			files[i] = DraftFile{Target: target}
		}
		t.workerDrafts(ctx, ref.SessionID, ref.JobID, api.AgentWorkerDraftStateReserved, files)
	})
}

// WorkerReleased drops reservations a worker session released; no targets releases them all.
func (t *Tracker) WorkerReleased(ctx context.Context, sessionID string, targets []Target) {
	if t == nil {
		return
	}
	ctx = context.WithoutCancel(ctx)
	t.enqueue(func() {
		ref, ok := t.chat(ctx, sessionID)
		if !ok || ref.JobID == "" {
			return
		}
		t.mutate(ctx, ref, func(c *chatPresence) bool {
			before := len(c.drafts)
			c.drafts = slices.DeleteFunc(c.drafts, func(d api.AgentWorkerDraft) bool {
				if d.WorkerID != ref.JobID || d.State != api.AgentWorkerDraftStateReserved {
					return false
				}
				return len(targets) == 0 || slices.Contains(targets, Target{RootID: d.RootID, Path: d.Path})
			})
			return len(c.drafts) != before
		})
	})
}

// ObserveDelivered follows worker drafts from committed worker and source events.
func (t *Tracker) ObserveDelivered(ctx context.Context, topic api.EventTopic, data json.RawMessage) error {
	if t == nil {
		return nil
	}
	ctx = context.WithoutCancel(ctx)
	switch topic {
	case api.EventTopicWorker:
		var ev api.WorkerEvent
		if err := json.Unmarshal(data, &ev); err != nil {
			return err
		}
		t.enqueue(func() { t.workerEvent(ctx, ev) })
	case api.EventTopicSourceChanged:
		var ev api.SourceChangesEvent
		if err := json.Unmarshal(data, &ev); err != nil {
			return err
		}
		if ev.WorkspaceKind == api.SourceWorkspaceKindWorker {
			t.enqueue(func() { t.workerSourceChanges(ctx, ev) })
		}
	default:
	}
	return nil
}

func (t *Tracker) workerEvent(ctx context.Context, ev api.WorkerEvent) {
	chat := strings.TrimSpace(ev.ParentSessionID)
	job := strings.TrimSpace(ev.WorkerID)
	if chat == "" || job == "" {
		return
	}
	switch ev.MergeStatus {
	case api.WorkerMergeStatusPending:
		if ev.Status != api.WorkerStatusComplete || t.drafts == nil {
			return
		}
		files, err := t.drafts.ReadyFiles(ctx, job)
		if err != nil {
			return
		}
		t.workerReady(ctx, chat, job, files)
	case api.WorkerMergeStatusApplying:
		t.workerLanding(ctx, chat, job)
	case api.WorkerMergeStatusMerged, api.WorkerMergeStatusRejected, api.WorkerMergeStatusAborted, api.WorkerMergeStatusOrphaned:
		t.workerSettled(ctx, chat, job)
	default:
		if ev.MergeStatus == "" && (ev.Status == api.WorkerStatusFailed || ev.Status == api.WorkerStatusCanceled) {
			t.workerSettled(ctx, chat, job)
		}
	}
}

func (t *Tracker) workerSourceChanges(ctx context.Context, ev api.SourceChangesEvent) {
	type jobKey struct{ session, job string }
	byJob := map[jobKey][]DraftFile{}
	var order []jobKey
	for _, change := range ev.Changes {
		if change.WorkerID == "" || change.SessionID == "" || change.Op == api.SourceChangeOpDelete && change.IsDir != nil && *change.IsDir {
			continue
		}
		key := jobKey{session: change.SessionID, job: change.WorkerID}
		if _, seen := byJob[key]; !seen {
			order = append(order, key)
		}
		byJob[key] = append(byJob[key], DraftFile{Target: Target{RootID: change.RootID, Path: change.Path}})
	}
	for _, key := range order {
		ref, ok := t.chat(ctx, key.session)
		if !ok {
			continue
		}
		t.workerDrafts(ctx, ref.SessionID, key.job, api.AgentWorkerDraftStateDrafting, byJob[key])
	}
}

// ReadyJob is a completed worker job whose promotion has not landed.
type ReadyJob struct {
	// ChatSessionID is the session that dispatched the job.
	ChatSessionID string
	JobID         string
}

// RestoreReadyDrafts publishes drafts for jobs that completed before the host
// started. Presence lives in memory, but a pending promotion outlives it.
func (t *Tracker) RestoreReadyDrafts(ctx context.Context, jobs []ReadyJob) {
	if t == nil || len(jobs) == 0 {
		return
	}
	ctx = context.WithoutCancel(ctx)
	t.enqueue(func() {
		if t.drafts == nil {
			return
		}
		for _, job := range jobs {
			files, err := t.drafts.ReadyFiles(ctx, job.JobID)
			if err != nil {
				continue
			}
			t.workerReady(ctx, job.ChatSessionID, job.JobID, files)
		}
	})
}
