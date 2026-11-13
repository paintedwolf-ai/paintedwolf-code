// Package workerroster projects a coordinator's workers into the bounded
// roster its every request carries. Each row reads the same derived state
// admission uses, so the roster never shows a worker as able to do something
// the host would refuse.
package workerroster

import (
	"context"
	"slices"
	"sort"
	"strings"

	"github.com/lycaon/lycaon/internal/workerops"
	"github.com/lycaon/lycaon/internal/workerrecords"
	"github.com/lycaon/lycaon/internal/workerstate"
	"github.com/lycaon/lycaon/pkg/api"
)

// Bounds keep the roster small; the full story is a worker_history read.
const (
	MaxRows      = 12
	MaxPaths     = 6
	MaxAboutRune = 160
)

// Row is one worker as the coordinator's request shows it.
type Row struct {
	Name      string
	Agent     string
	ScopeMode string
	State     workerstate.State
	Reason    workerstate.Reason
	// About is what the worker is on: its latest instruction, else its brief.
	About string
	// Question is the pending decision a worker in asking waits on.
	Question string
	Result   *Result
	Read     Paths
	Changed  Paths
}

// Result is the worker's latest result and what became of it.
type Result struct {
	Handle string
	// Disposition is unaccounted, accounted, landed, rejected, shelved, or
	// open for changes neither landed, rejected nor shelved.
	Disposition string
}

// Paths is a bounded path list and how many it left out.
type Paths struct {
	Shown   []string
	Omitted int
}

// Roster is the bounded set of rows and how many workers it left out.
type Roster struct {
	Rows    []Row
	Omitted int
}

// Touches reports the paths a job's worker has read.
type Touches interface {
	Paths(jobID string) []string
}

// Source gathers rows for a coordinator session.
type Source struct {
	Reader  workerops.Reader
	Touches Touches
}

// Build returns the roster for a coordinator session. Closed workers are left
// out; rows needing the coordinator come first.
func (s Source) Build(ctx context.Context, sessionID string) (Roster, error) {
	actor := workerrecords.Actor{SessionID: sessionID, Role: workerrecords.RoleCoordinator, PrincipalID: sessionID}
	workers, err := s.Reader.Records.Workers(ctx, actor)
	if err != nil {
		return Roster{}, err
	}
	var rows []Row
	for _, w := range workers {
		snap, err := s.Reader.Read(ctx, actor, w)
		if err != nil {
			return Roster{}, err
		}
		if snap.View.State == workerstate.Closed {
			continue
		}
		row, err := s.row(ctx, actor, snap)
		if err != nil {
			return Roster{}, err
		}
		rows = append(rows, row)
	}
	sort.SliceStable(rows, func(i, j int) bool {
		if a, b := priority(rows[i].State), priority(rows[j].State); a != b {
			return a < b
		}
		return rows[i].Name < rows[j].Name
	})
	out := Roster{Rows: rows}
	if len(rows) > MaxRows {
		out.Rows, out.Omitted = rows[:MaxRows], len(rows)-MaxRows
	}
	return out, nil
}

// priority orders states by how much they need the coordinator.
func priority(s workerstate.State) int {
	order := []workerstate.State{
		workerstate.Asking, workerstate.Recovering, workerstate.Failed, workerstate.Delivered,
		workerstate.Working, workerstate.Waiting, workerstate.Planned, workerstate.Idle,
	}
	if i := slices.Index(order, s); i >= 0 {
		return i
	}
	return len(order)
}

func (s Source) row(ctx context.Context, actor workerrecords.Actor, snap workerops.Snapshot) (Row, error) {
	w := snap.Worker
	row := Row{
		Name: w.Name, Agent: w.AgentType, ScopeMode: w.ScopeMode,
		State: snap.View.State, Reason: snap.View.Reason,
	}
	about, err := s.about(ctx, actor, snap)
	if err != nil {
		return Row{}, err
	}
	row.About = about
	if row.State == workerstate.Asking && s.Reader.Decisions != nil && w.ChildSessionID != "" {
		req, ok, err := s.Reader.Decisions.Get(ctx, w.ChildSessionID)
		if err != nil {
			return Row{}, err
		}
		if ok {
			row.Question = clip(req.Question)
		}
	}
	if round, ok := snap.LatestResult(); ok {
		row.Result = &Result{Handle: round.ResultID, Disposition: disposition(snap, round)}
	}
	var read, changed []string
	for _, round := range snap.Rounds {
		if s.Touches != nil {
			read = append(read, s.Touches.Paths(round.JobID)...)
		}
		changed = append(changed, s.changedPaths(round.JobID)...)
	}
	row.Read, row.Changed = bound(read), bound(changed)
	return row, nil
}

// about is the latest instruction a worker was started with, else its brief.
func (s Source) about(ctx context.Context, actor workerrecords.Actor, snap workerops.Snapshot) (string, error) {
	if latest, ok := snap.Latest(); ok && latest.InstructionLastSeq > 0 {
		msgs, err := s.Reader.Records.History(ctx, actor, snap.Worker.ID, latest.InstructionLastSeq+1, 1)
		if err != nil {
			return "", err
		}
		if len(msgs) > 0 && strings.TrimSpace(msgs[0].Body) != "" {
			return clip(msgs[0].Body), nil
		}
	}
	return clip(snap.Worker.Brief), nil
}

func (s Source) changedPaths(jobID string) []string {
	if s.Reader.Jobs == nil {
		return nil
	}
	job, ok := s.Reader.Jobs.Get(jobID)
	if !ok || job == nil || job.Result == nil || job.Result.ChangeReport == nil {
		return nil
	}
	return job.Result.ChangeReport.ChangedPaths
}

func disposition(snap workerops.Snapshot, round workerrecords.Round) string {
	d := snap.Dispositions[round.ResultID]
	switch {
	case d.ChangeState != "":
		return d.ChangeState
	case round.CheckpointID != "":
		return "open"
	case d.AccountedAt != "":
		return "accounted"
	}
	return "unaccounted"
}

// bound dedupes paths in first-seen order and keeps MaxPaths of them.
func bound(paths []string) Paths {
	seen := map[string]bool{}
	var unique []string
	for _, p := range paths {
		if p = strings.TrimSpace(p); p != "" && !seen[p] {
			seen[p] = true
			unique = append(unique, p)
		}
	}
	if len(unique) > MaxPaths {
		return Paths{Shown: unique[:MaxPaths], Omitted: len(unique) - MaxPaths}
	}
	return Paths{Shown: unique}
}

// clip keeps the first line of text within MaxAboutRune runes.
func clip(text string) string {
	text = strings.TrimSpace(text)
	if i := strings.IndexByte(text, '\n'); i >= 0 {
		text = strings.TrimSpace(text[:i]) + " …"
	}
	if r := []rune(text); len(r) > MaxAboutRune {
		text = string(r[:MaxAboutRune]) + "…"
	}
	return text
}

// Vars renders the roster for templates: one map per row.
func (r Roster) Vars() []map[string]any {
	out := make([]map[string]any, 0, len(r.Rows))
	for _, row := range r.Rows {
		m := map[string]any{
			"name": row.Name, "agent": row.Agent, "scope_mode": row.ScopeMode,
			"state": string(row.State), "reason": strings.ReplaceAll(string(row.Reason), "_", " "), "about": row.About,
			"question": row.Question, "read": row.Read.Shown, "read_omitted": row.Read.Omitted,
			"changed": row.Changed.Shown, "changed_omitted": row.Changed.Omitted,
		}
		if row.Result != nil {
			m["result_handle"] = row.Result.Handle
			m["result_disposition"] = row.Result.Disposition
		}
		out = append(out, m)
	}
	return out
}

// Template builds the roster as template rows for a request frame.
func (s Source) Template(ctx context.Context, sessionID string) ([]map[string]any, int, error) {
	r, err := s.Build(ctx, sessionID)
	if err != nil {
		return nil, 0, err
	}
	return r.Vars(), r.Omitted, nil
}

// ResultNotice describes a settled round for its coordinator's notice: the
// worker, its result handle, the report status, the changed paths, and the
// instructions the round never received. ok is false for a job that runs no
// worker round.
func (s Source) ResultNotice(ctx context.Context, jobID string) (map[string]any, bool, error) {
	round, ok, err := s.Reader.Records.RoundByJob(ctx, jobID)
	if err != nil || !ok || s.Reader.Jobs == nil {
		return nil, false, err
	}
	job, found := s.Reader.Jobs.Get(jobID)
	if !found || job == nil {
		return nil, false, nil
	}
	actor := workerrecords.Actor{SessionID: job.ParentSessionID, Role: workerrecords.RoleCoordinator, PrincipalID: job.ParentSessionID}
	w, err := s.Reader.Records.Worker(ctx, actor, round.WorkerID)
	if err != nil {
		return nil, false, err
	}
	rounds, err := s.Reader.Records.Rounds(ctx, actor, w.ID)
	if err != nil {
		return nil, false, err
	}
	notice := map[string]any{"worker": w.Name, "leg_status": api.WorkerTaskLegStatus(*job)}
	for _, r := range rounds {
		if r.Ordinal == round.Ordinal && r.ResultID != "" {
			notice["result_handle"] = r.ResultID
		}
	}
	changed := bound(s.changedPaths(jobID))
	notice["changed"], notice["changed_omitted"] = changed.Shown, changed.Omitted
	missed, err := s.Reader.Records.NotApplied(ctx, actor, w.ID, round.Ordinal)
	if err != nil {
		return nil, false, err
	}
	var bodies []string
	for _, m := range missed {
		bodies = append(bodies, clip(m.Body))
	}
	notice["not_applied"] = bodies
	return notice, true, nil
}
