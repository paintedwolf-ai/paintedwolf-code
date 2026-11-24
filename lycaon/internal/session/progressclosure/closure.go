package progressclosure

import (
	"context"
	"database/sql"
	"slices"
	"strconv"
	"strings"
	"sync"

	"github.com/lycaon/lycaon/internal/coordinator/guard"
	"github.com/lycaon/lycaon/internal/progress"
	"github.com/lycaon/lycaon/internal/runeclamp"
	"github.com/lycaon/lycaon/internal/scopedstore"
	"github.com/lycaon/lycaon/internal/workerrecords"
	"github.com/lycaon/lycaon/pkg/api"
)

type Progress interface {
	Snapshot(context.Context, string) (progress.Snapshot, error)
}

type Tasks interface {
	Get(string) (*api.WorkerTask, bool)
}

// Names resolves the worker name and result handle for a job.
type Names interface {
	NamesByJob(ctx context.Context, jobID string) (workerrecords.JobNames, bool, error)
}

// Accounts resolves and records acknowledged worker results inside a SQLite transaction.
type Accounts interface {
	ResolveResultForAckTx(ctx context.Context, tx *sql.Tx, actor workerrecords.Actor, resultID string) (workerrecords.ResultRef, error)
	AccountJobsTx(ctx context.Context, tx *sql.Tx, jobIDs []string) error
}

type Service struct {
	mu        sync.Mutex
	progress  Progress
	tasks     Tasks
	accounts  Accounts
	names     Names
	baselines scopedstore.LRU[guard.ProgressClosureBaseline]
}

var _ progress.TxReconciler = (*Service)(nil)

func New() *Service                       { return &Service{} }
func (m *Service) SetProgress(p Progress) { m.progress = p }
func (m *Service) SetTasks(p Tasks)       { m.tasks = p }
func (m *Service) SetNames(n Names)       { m.names = n }
func (m *Service) SetAccounts(a Accounts) { m.accounts = a }

func (m *Service) Forget(id string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.baselines.Delete(id)
}

// Arm latches a completed worker job until its result is accounted for.
func (m *Service) Arm(ctx context.Context, rootID, jobID string) {
	if m == nil || m.progress == nil {
		return
	}
	rootID = strings.TrimSpace(rootID)
	if rootID == "" {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	snapshot, err := m.progress.Snapshot(ctx, rootID)
	if err != nil {
		return
	}
	pending := snapshot.OpenStepCount()
	baseline, armed := m.baselines.Load(rootID)
	if pending == 0 && !armed {
		return
	}
	if jobID != "" && !slices.Contains(baseline.JobIDs, jobID) {
		settled, unaccounted := m.describeFinish(ctx, jobID, len(baseline.JobIDs)+1)
		baseline.JobIDs = append(slices.Clone(baseline.JobIDs), jobID)
		baseline.Settled = append(slices.Clone(baseline.Settled), settled)
		baseline.Unaccounted = append(slices.Clone(baseline.Unaccounted), unaccounted)
	}
	m.baselines.Store(rootID, baseline)
}

// Release removes accounted jobs from the latch, clearing the baseline when none remain.
func (m *Service) Release(rootID string, jobIDs []string) {
	if m == nil || len(jobIDs) == 0 {
		return
	}
	rootID = strings.TrimSpace(rootID)
	m.mu.Lock()
	defer m.mu.Unlock()
	baseline, armed := m.baselines.Load(rootID)
	if !armed {
		return
	}
	kept := make([]string, 0, len(baseline.JobIDs))
	settled := make([]string, 0, len(baseline.JobIDs))
	unaccounted := make([]progress.UnaccountedResult, 0, len(baseline.JobIDs))
	for i, id := range baseline.JobIDs {
		if slices.Contains(jobIDs, id) {
			continue
		}
		kept = append(kept, id)
		if i < len(baseline.Settled) {
			settled = append(settled, baseline.Settled[i])
		} else {
			settled = append(settled, "")
		}
		if i < len(baseline.Unaccounted) {
			unaccounted = append(unaccounted, baseline.Unaccounted[i])
		}
	}
	if len(kept) == 0 {
		m.baselines.Delete(rootID)
		return
	}
	baseline.JobIDs = kept
	baseline.Settled = settled
	baseline.Unaccounted = unaccounted
	m.baselines.Store(rootID, baseline)
}

func (m *Service) describeFinish(ctx context.Context, jobID string, seq int) (string, progress.UnaccountedResult) {
	var name, handle, agent, brief, summary string
	if m.names != nil {
		if names, ok, err := m.names.NamesByJob(ctx, jobID); err == nil && ok {
			name = strings.TrimSpace(names.Worker)
			handle = strings.TrimSpace(names.Handle)
		}
	}
	if m.tasks != nil {
		if task, ok := m.tasks.Get(jobID); ok && task != nil {
			agent = strings.TrimSpace(task.AgentType)
			brief = strings.TrimSpace(task.Brief)
			if task.Result != nil && strings.TrimSpace(task.Result.Summary) != "" {
				summary = strings.TrimSpace(task.Result.Summary)
			} else {
				summary = brief
			}
		}
	}
	if handle == "" {
		if name != "" {
			handle = name + "#1"
		} else {
			handle = "result#" + strconv.Itoa(seq)
		}
	}
	entry := agent
	if name != "" {
		entry = "`" + name + "`"
		if agent != "" {
			entry += " (" + agent + ")"
		}
	}
	if brief != "" {
		if entry == "" {
			entry = brief
		} else {
			entry += ": " + brief
		}
	}
	return entry, progress.UnaccountedResult{
		Handle:  handle,
		Worker:  name,
		Summary: runeclamp.Clamp(summary, 160),
	}
}

// Baseline returns the armed checklist baseline for rootID.
func (m *Service) Baseline(rootID string) (baseline guard.ProgressClosureBaseline, armed bool) {
	if m == nil {
		return guard.ProgressClosureBaseline{}, false
	}
	rootID = strings.TrimSpace(rootID)
	if rootID == "" {
		return guard.ProgressClosureBaseline{}, false
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	exp, ok := m.baselines.Load(rootID)
	if !ok {
		return guard.ProgressClosureBaseline{}, false
	}
	return exp, true
}
