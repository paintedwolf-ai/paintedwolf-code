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
	Get(context.Context, string) string
}
type Tasks interface {
	Get(string) (*api.WorkerTask, bool)
}
type Service struct {
	mu        sync.Mutex
	progress  Progress
	tasks     Tasks
	baselines scopedstore.LRU[guard.ProgressClosureBaseline]
}

func New() *Service                       { return &Service{} }
func (m *Service) SetProgress(p Progress) { m.progress = p }
func (m *Service) SetTasks(p Tasks)       { m.tasks = p }
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
	content := m.progress.Get(ctx, rootID)
	done, pending, na := progress.CloseCounts(content)
	baseline, armed := m.baselines.Load(rootID)
	if pending == 0 && !armed {
		return
	}
	if !armed {
		baseline = guard.ProgressClosureBaseline{Closed: done + na, Content: content}
	}
	if entry := m.settledWorkEntry(jobID); entry != "" && !slices.Contains(baseline.Settled, entry) {
		baseline.Settled = append(slices.Clone(baseline.Settled), entry)
	}
	if jobID != "" && !slices.Contains(baseline.JobIDs, jobID) {
		baseline.JobIDs = append(slices.Clone(baseline.JobIDs), jobID)
	}
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
