package workerops

import (
	"context"
	"encoding/json"
	"errors"
	"strings"

	"github.com/lycaon/lycaon/internal/workerrecords"
	"github.com/lycaon/lycaon/internal/workerstate"
)

// Refusal is an operation the worker's state does not admit.
type Refusal struct {
	Worker string
	Op     workerstate.Operation
	View   workerstate.View
	// AgentChange is set when an agent or scope change was refused.
	AgentChange workerstate.AgentChange
	// JobID is the worker's latest round's job; ChangedJobID is the job of
	// its newest result whose changes are open, else of its newest changed
	// result; WorkflowWorkID is a planned leg's work identity. Recovery
	// steps are built from them.
	JobID, ChangedJobID, WorkflowWorkID string
}

func refusal(snap Snapshot, op workerstate.Operation) *Refusal {
	r := &Refusal{Worker: snap.Worker.Name, Op: op, View: snap.View, WorkflowWorkID: snap.Worker.WorkflowWorkID}
	if latest, ok := snap.Latest(); ok {
		r.JobID = latest.JobID
	}
	for i := len(snap.Rounds) - 1; i >= 0; i-- {
		round := snap.Rounds[i]
		if round.ResultID == "" || round.CheckpointID == "" {
			continue
		}
		if r.ChangedJobID == "" {
			r.ChangedJobID = round.JobID
		}
		if snap.Dispositions[round.ResultID].ChangeState == "" {
			r.ChangedJobID = round.JobID
			break
		}
	}
	return r
}

func (r *Refusal) Error() string {
	if r.AgentChange != workerstate.AgentChangeAdmitted {
		return "worker " + r.Worker + " cannot change agent: " + string(r.AgentChange)
	}
	return "worker " + r.Worker + " is " + string(r.View.State) + " and does not admit " + string(r.Op)
}

// Outcome says what an instruction did.
type Outcome string

const (
	// OutcomeQueued delivers the instruction to the running round before its
	// next model call.
	OutcomeQueued Outcome = "queued"
	// OutcomeStarted admitted a new round in the same conversation.
	OutcomeStarted Outcome = "started"
)

// Instruction is what the coordinator sends.
type Instruction struct {
	Worker       string
	Instructions string
	// Brief replaces the standing brief when the goal changes.
	Brief string
	// AgentType changes the agent between rounds; scope mode follows its lane.
	AgentType string
}

// RoundJob is the job a new round runs on.
type RoundJob struct {
	Worker       workerrecords.Worker
	Instructions string
	Brief        string
	AgentType    string
	ScopeMode    string
}

// Enqueuer enqueues a round's job on the worker's child session and returns
// its id; a replayed call returns the job it enqueued before.
type Enqueuer func(ctx context.Context, job RoundJob) (jobID, executionTarget string, err error)

// AgentLane reports whether an agent writes, and whether it is known.
type AgentLane func(agentType string) (write, ok bool)

// Receipt says what an instruction did.
type Receipt struct {
	Worker         string            `json:"worker"`
	Outcome        Outcome           `json:"outcome"`
	State          workerstate.State `json:"state"`
	Round          int64             `json:"round,omitempty"`
	JobID          string            `json:"job_id,omitempty"`
	InstructionSeq int64             `json:"instruction_seq"`
	// Accounted names the result this instruction accounted for.
	Accounted string `json:"accounted,omitempty"`
}

var (
	// ErrUnknownAgent reports an agent change to an agent that does not exist.
	ErrUnknownAgent = errors.New("unknown agent type")
	// ErrNoChanges reports a result whose round changed nothing.
	ErrNoChanges = errors.New("result has no changes")
)

// Service carries out coordinator operations on workers.
type Service struct {
	Reader Reader
	Agents AgentLane
	// Accounted hears of results an operation accounted for, by job, so the
	// checklist latch can release them.
	Accounted func(sessionID string, jobIDs []string)
}

func (s *Service) accounted(sessionID, jobID string) {
	if s.Accounted != nil && jobID != "" {
		s.Accounted(sessionID, []string{jobID})
	}
}

func (s *Service) resolve(ctx context.Context, actor workerrecords.Actor, name string) (Snapshot, error) {
	w, err := s.Reader.Records.Resolve(ctx, actor, strings.TrimSpace(name))
	if err != nil {
		return Snapshot{}, err
	}
	return s.Reader.Read(ctx, actor, w)
}

// Instruct queues an instruction for a running round, or starts a new round
// for a worker between rounds. An instruction to a delivered worker accounts
// for its latest complete result: the deliverable stays open on the checklist.
func (s *Service) Instruct(ctx context.Context, actor workerrecords.Actor, op workerrecords.Op, in Instruction, enqueue Enqueuer) (Receipt, error) {
	in.Instructions = strings.TrimSpace(in.Instructions)
	in.Brief = strings.TrimSpace(in.Brief)
	in.AgentType = strings.TrimSpace(in.AgentType)
	if in.Instructions == "" {
		return Receipt{}, workerrecords.ErrInvalidMessage
	}
	snap, err := s.resolve(ctx, actor, in.Worker)
	if err != nil {
		return Receipt{}, err
	}
	if prior, ok, err := s.replay(ctx, actor, op, snap); err != nil || ok {
		return prior, err
	}
	w, view := snap.Worker, snap.View
	if !view.Admits(workerstate.OpInstruct) {
		return Receipt{}, refusal(snap, workerstate.OpInstruct)
	}
	scope := ""
	if in.AgentType != "" && in.AgentType != w.AgentType {
		write, ok := s.Agents(in.AgentType)
		if !ok {
			return Receipt{}, ErrUnknownAgent
		}
		scope = "read"
		if write {
			scope = "write"
		}
		if change := workerstate.CheckAgentChange(view, snap.Facts, w.ScopeMode == "write", write); change != workerstate.AgentChangeAdmitted {
			r := refusal(snap, workerstate.OpInstruct)
			r.AgentChange = change
			return Receipt{}, r
		}
	} else {
		in.AgentType = ""
	}
	if view.State == workerstate.Working {
		if in.Brief != "" {
			if _, err := s.Reader.Records.SetBrief(ctx, actor, workerrecords.Op{ID: op.ID + ":brief"}, w.ID, in.Brief); err != nil {
				return Receipt{}, err
			}
		}
		m, err := s.Reader.Records.AppendMessage(ctx, actor, op, w.ID, workerrecords.MessageSpec{Body: in.Instructions})
		if err != nil {
			return Receipt{}, err
		}
		latest, _ := snap.Latest()
		return Receipt{Worker: w.Name, Outcome: OutcomeQueued, State: view.State, Round: latest.Ordinal, JobID: latest.JobID, InstructionSeq: m.Seq}, nil
	}
	jobID, target, err := enqueue(ctx, RoundJob{Worker: w, Instructions: in.Instructions, Brief: in.Brief, AgentType: in.AgentType, ScopeMode: scope})
	if err != nil {
		return Receipt{}, err
	}
	start, err := s.Reader.Records.StartRound(ctx, actor, op, w.ID, workerrecords.RoundStart{
		JobID: jobID, ExecutionTarget: target, Instruction: in.Instructions, Brief: in.Brief, AgentType: in.AgentType, ScopeMode: scope,
	})
	if err != nil {
		return Receipt{}, err
	}
	receipt := Receipt{Worker: w.Name, Outcome: OutcomeStarted, State: workerstate.Working, Round: start.Ordinal, JobID: jobID, InstructionSeq: start.InstructionSeq}
	if view.Admits(workerstate.OpAccount) {
		if result, ok := snap.LatestResult(); ok && snap.Dispositions[result.ResultID].AccountedAt == "" {
			if _, err := s.Reader.Records.AccountResult(ctx, actor, workerrecords.Op{ID: op.ID + ":account"}, result.ResultID); err != nil && !errors.Is(err, workerrecords.ErrAlreadyAccounted) {
				return Receipt{}, err
			}
			receipt.Accounted = result.ResultID
			s.accounted(actor.SessionID, result.JobID)
		}
	}
	return receipt, nil
}

// replay returns what an instruction already did under this operation
// identity: the worker's state has moved on since, so it is not re-derived.
func (s *Service) replay(ctx context.Context, actor workerrecords.Actor, op workerrecords.Op, snap Snapshot) (Receipt, bool, error) {
	name := snap.Worker.Name
	kind, raw, ok, err := s.Reader.Records.Operation(ctx, actor, op.ID)
	if err != nil || !ok {
		return Receipt{}, false, err
	}
	switch kind {
	case "start_round":
		var start workerrecords.RoundStartReceipt
		if err := json.Unmarshal(raw, &start); err != nil {
			return Receipt{}, false, err
		}
		receipt := Receipt{Worker: name, Outcome: OutcomeStarted, State: workerstate.Working, Round: start.Ordinal, JobID: start.JobID, InstructionSeq: start.InstructionSeq}
		if akind, araw, aok, err := s.Reader.Records.Operation(ctx, actor, op.ID+":account"); err != nil {
			return Receipt{}, false, err
		} else if aok && akind == "account_result" {
			var d workerrecords.Disposition
			if err := json.Unmarshal(araw, &d); err != nil {
				return Receipt{}, false, err
			}
			receipt.Accounted = d.ResultID
		}
		return receipt, true, nil
	case "append_message":
		var m workerrecords.MessageReceipt
		if err := json.Unmarshal(raw, &m); err != nil {
			return Receipt{}, false, err
		}
		receipt := Receipt{Worker: name, Outcome: OutcomeQueued, State: workerstate.Working, Round: m.RoundOrdinal, InstructionSeq: m.Seq}
		for _, r := range snap.Rounds {
			if r.Ordinal == m.RoundOrdinal {
				receipt.JobID = r.JobID
			}
		}
		return receipt, true, nil
	}
	return Receipt{}, false, workerrecords.ErrOperationConflict
}

// Shelve leaves a result's changes unlanded on purpose and accounts for it.
func (s *Service) Shelve(ctx context.Context, actor workerrecords.Actor, op workerrecords.Op, resultID, reason string) (workerrecords.Disposition, error) {
	reason = strings.TrimSpace(reason)
	if reason == "" {
		return workerrecords.Disposition{}, workerrecords.ErrReasonRequired
	}
	ref, err := s.Reader.Records.Result(ctx, actor, resultID)
	if err != nil {
		return workerrecords.Disposition{}, err
	}
	w, err := s.Reader.Records.Worker(ctx, actor, ref.WorkerID)
	if err != nil {
		return workerrecords.Disposition{}, err
	}
	snap, err := s.Reader.Read(ctx, actor, w)
	if err != nil {
		return workerrecords.Disposition{}, err
	}
	if !snap.View.Admits(workerstate.OpShelve) {
		return workerrecords.Disposition{}, refusal(snap, workerstate.OpShelve)
	}
	changed := false
	for _, r := range snap.Rounds {
		changed = changed || (r.ResultID == ref.ResultID && r.CheckpointID != "")
	}
	if !changed {
		return workerrecords.Disposition{}, ErrNoChanges
	}
	d, err := s.Reader.Records.SetChange(ctx, actor, op, ref.ResultID, workerrecords.Change{State: workerrecords.ChangeShelved, ShelveReason: reason})
	if err != nil {
		return workerrecords.Disposition{}, err
	}
	if d.AccountedAt == "" {
		if d, err = s.Reader.Records.AccountResult(ctx, actor, workerrecords.Op{ID: op.ID + ":account"}, ref.ResultID); err != nil && !errors.Is(err, workerrecords.ErrAlreadyAccounted) {
			return workerrecords.Disposition{}, err
		}
	}
	s.accounted(actor.SessionID, ref.JobID)
	return d, nil
}

// HistoryPage is one page of a worker's story, newest first.
type HistoryPage struct {
	Worker   workerrecords.Worker
	State    workerstate.View
	Rounds   []workerrecords.Round
	Messages []workerrecords.Message
	// Before continues to older messages; zero when none are left.
	Before int64
}

// History reads a worker's rounds and a page of messages before a sequence.
// Reading resolves and acknowledges nothing.
func (s *Service) History(ctx context.Context, actor workerrecords.Actor, name string, before int64, limit int) (HistoryPage, error) {
	if limit <= 0 || limit >= workerrecords.MaxPageMessages {
		limit = workerrecords.MaxPageMessages - 1
	}
	snap, err := s.resolve(ctx, actor, name)
	if err != nil {
		return HistoryPage{}, err
	}
	msgs, err := s.Reader.Records.History(ctx, actor, snap.Worker.ID, before, limit+1)
	if err != nil {
		return HistoryPage{}, err
	}
	page := HistoryPage{Worker: snap.Worker, State: snap.View, Rounds: snap.Rounds, Messages: msgs}
	if len(msgs) > limit {
		page.Messages = msgs[:limit]
		page.Before = msgs[limit-1].Seq
	}
	return page, nil
}
