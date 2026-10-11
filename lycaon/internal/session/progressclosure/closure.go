package progressclosure

import (
	"context"
	"slices"
	"strings"

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
	progress  Progress
	tasks     Tasks
	baselines scopedstore.LRU[guard.ProgressClosureBaseline]
}

func New() *Service                       { return &Service{} }
func (m *Service) SetProgress(p Progress) { m.progress = p }
func (m *Service) SetTasks(p Tasks)       { m.tasks = p }
func (m *Service) Forget(id string)       { m.baselines.Delete(id) }

// Arm latches the checklist after a worker settles its
// deliverable. Only a successful finish arms it: a partial, failed, or held
// leg has closed nothing, and its resume is the same deliverable continuing.
// The first baseline holds until the checklist changes; later successes join
// the list of finishes the reconciliation must account for.
func (m *Service) Arm(ctx context.Context, rootID, jobID string) {
	if m == nil || m.progress == nil {
		return
	}
	rootID = strings.TrimSpace(rootID)
	if rootID == "" {
		return
	}
	content := m.progress.Get(ctx, rootID)
	done, pending, na := progress.CloseCounts(content)
	if pending == 0 {
		m.baselines.Delete(rootID)
		return
	}
	baseline, armed := m.baselines.Load(rootID)
	if !armed {
		baseline = guard.ProgressClosureBaseline{Closed: done + na, Content: content}
	}
	if entry := m.settledWorkEntry(jobID); entry != "" && !slices.Contains(baseline.Settled, entry) {
		baseline.Settled = append(slices.Clone(baseline.Settled), entry)
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
	exp, ok := m.baselines.Load(rootID)
	if !ok {
		return guard.ProgressClosureBaseline{}, false
	}
	return exp, true
}

func (m *Service) ClearSatisfied(rootID, progressContent string, baseline guard.ProgressClosureBaseline) {
	if m == nil {
		return
	}
	rootID = strings.TrimSpace(rootID)
	if rootID == "" {
		return
	}
	done, pending, na := progress.CloseCounts(progressContent)
	if pending == 0 || done+na > baseline.Closed || progressContent != baseline.Content {
		m.baselines.Delete(rootID)
	}
}

// Unchanged checklist writes keep the reconciliation latch armed.
func (m *Service) AfterWrite(ctx context.Context, rootID string) {
	if m == nil || m.progress == nil {
		return
	}
	rootID = strings.TrimSpace(rootID)
	if rootID == "" {
		return
	}
	baseline, armed := m.Baseline(rootID)
	if !armed {
		return
	}
	m.ClearSatisfied(rootID, m.progress.Get(ctx, rootID), baseline)
}
