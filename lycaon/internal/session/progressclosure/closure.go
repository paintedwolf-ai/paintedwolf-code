package progressclosure

import (
	"context"
	"slices"
	"strings"
	"sync"

	"github.com/lycaon/lycaon/internal/coordinator/guard"
	"github.com/lycaon/lycaon/internal/progress"
	"github.com/lycaon/lycaon/internal/scopedstore"
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
	baselines scopedstore.LRU[guard.ProgressClosureBaseline]
}

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
	if entry := m.settledWorkEntry(jobID); entry != "" && !slices.Contains(baseline.Settled, entry) {
		baseline.Settled = append(slices.Clone(baseline.Settled), entry)
	}
	if jobID != "" && !slices.Contains(baseline.JobIDs, jobID) {
		baseline.JobIDs = append(slices.Clone(baseline.JobIDs), jobID)
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
	for _, id := range baseline.JobIDs {
		if !slices.Contains(jobIDs, id) {
			kept = append(kept, id)
		}
	}
	if len(kept) == 0 {
		m.baselines.Delete(rootID)
		return
	}
	settled := make([]string, 0, len(baseline.Settled))
	for _, entry := range baseline.Settled {
		if !slices.ContainsFunc(jobIDs, func(id string) bool { return entry == m.settledWorkEntry(id) }) {
			settled = append(settled, entry)
		}
	}
	baseline.JobIDs, baseline.Settled = kept, settled
	m.baselines.Store(rootID, baseline)
}

// settledWorkEntry names a finished job for the reconciliation reject.
func (m *Service) settledWorkEntry(jobID string) string {
	jobID = strings.TrimSpace(jobID)
	if jobID == "" || m.tasks == nil {
		return jobID
	}
	task, ok := m.tasks.Get(jobID)
	if !ok || task == nil {
		return jobID
	}
	entry := task.AgentType + " `" + jobID + "`"
	if brief := strings.TrimSpace(task.Brief); brief != "" {
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
