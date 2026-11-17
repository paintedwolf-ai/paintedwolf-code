package progressclosure

import (
	"context"
	"slices"
	"strings"
	"sync"

	"github.com/lycaon/lycaon/internal/coordinator/guard"
	"github.com/lycaon/lycaon/internal/progress"
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
type Service struct {
	mu        sync.Mutex
	progress  Progress
	tasks     Tasks
	accounts  Accounts
	names     Names
	baselines scopedstore.LRU[guard.ProgressClosureBaseline]
}

// Names gives the worker that ran a job.
type Names interface {
	NamesByJob(ctx context.Context, jobID string) (workerrecords.JobNames, bool, error)
}

// SetNames wires the worker names finishes are reported by.
func (m *Service) SetNames(n Names) { m.names = n }

// Accounts records that the coordinator accounted for the results of the
// acknowledged worker finishes.
type Accounts interface {
	AccountJobs(ctx context.Context, jobIDs []string) error
}

func New() *Service                       { return &Service{} }
func (m *Service) SetProgress(p Progress) { m.progress = p }
func (m *Service) SetTasks(p Tasks)       { m.tasks = p }

// SetAccounts wires durable accounting of acknowledged results.
func (m *Service) SetAccounts(a Accounts) { m.accounts = a }
func (m *Service) Forget(id string)       { m.mu.Lock(); defer m.mu.Unlock(); m.baselines.Delete(id) }

// Arm records a successful worker finish against the current checklist.
// Later finishes join the same set until explicit acknowledgement.
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
	content := snapshot.Content
	_, pending, _ := progress.CloseCounts(content)
	baseline, armed := m.baselines.Load(rootID)
	if pending == 0 && !armed {
		return
	}
	if !armed {
		baseline = guard.ProgressClosureBaseline{Revision: snapshot.Revision}
	}
	if jobID != "" && !slices.Contains(baseline.JobIDs, jobID) {
		baseline.JobIDs = append(slices.Clone(baseline.JobIDs), jobID)
		baseline.Settled = append(slices.Clone(baseline.Settled), m.settledWorkEntry(ctx, jobID))
	}
	m.baselines.Store(rootID, baseline)
}

// Release removes finishes from the latch once an operation accounted for
// their results on the record: an instruction to the worker that delivered
// it, a rejection or a shelving. The latch clears with its last finish.
func (m *Service) Release(rootID string, jobIDs []string) {
	if m == nil {
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
	for i, id := range baseline.JobIDs {
		if !slices.Contains(jobIDs, id) {
			kept = append(kept, id)
			if i < len(baseline.Settled) {
				settled = append(settled, baseline.Settled[i])
			} else {
				settled = append(settled, "")
			}
		}
	}
	if len(kept) == 0 {
		m.baselines.Delete(rootID)
		return
	}
	baseline.JobIDs, baseline.Settled = kept, settled
	m.baselines.Store(rootID, baseline)
}

// settledWorkEntry names a finished job's worker for the reconciliation
// reject; the coordinator never addresses the job itself.
func (m *Service) settledWorkEntry(ctx context.Context, jobID string) string {
	var name, agent, brief string
	if m.names != nil {
		if names, ok, err := m.names.NamesByJob(ctx, jobID); err == nil && ok {
			name = names.Worker
		}
	}
	if m.tasks != nil {
		if task, ok := m.tasks.Get(jobID); ok && task != nil {
			agent, brief = strings.TrimSpace(task.AgentType), strings.TrimSpace(task.Brief)
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
			return brief
		}
		entry += ": " + brief
	}
	return entry
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
