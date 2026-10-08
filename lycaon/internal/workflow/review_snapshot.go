package workflow

import (
	"context"

	"github.com/lycaon/lycaon/internal/jsonvalue"
	workflowdef "github.com/lycaon/lycaon/internal/workflow/definition"
	"github.com/lycaon/lycaon/pkg/api"
)

// ReviewSnapshot freezes the evidence used by an incomplete report at pause.
// Its candidate remains explicitly separate from accepted verdict records.
type ReviewSnapshot struct {
	Vars        map[string]any   `json:"vars"`
	Workers     []api.WorkerTask `json:"workers"`
	Scans       []api.CodeScan   `json:"scans"`
	Verdicts    []PhaseVerdict   `json:"verdicts"`
	Candidate   map[string]any   `json:"candidate,omitempty"`
	Unavailable []string         `json:"unavailable,omitempty"`
}

func (m *RunManager) captureReviewSnapshot(ctx context.Context, run *api.WorkflowRun, manifest workflowdef.Manifest, vars map[string]any, candidate map[string]any) ReviewSnapshot {
	snapshot := ReviewSnapshot{Vars: map[string]any{}, Candidate: jsonvalue.CloneMap(candidate)}
	// Only report inputs are copied, excluding repair episodes themselves.
	for _, key := range []string{"fanout_plans"} {
		if value, ok := vars[key]; ok {
			snapshot.Vars[key] = value
		}
	}
	if m.WorkerTasks != nil {
		workers, err := m.WorkerTasks(ctx, run.ID)
		if err != nil {
			snapshot.Unavailable = append(snapshot.Unavailable, "worker ledger")
		} else {
			snapshot.Workers = workers
		}
	} else {
		snapshot.Unavailable = append(snapshot.Unavailable, "worker ledger")
	}
	if m.Inventory != nil {
		inventory, err := LoadRunInventory(ctx, m.Inventory, run.ID)
		if err != nil {
			snapshot.Unavailable = append(snapshot.Unavailable, "scan ledger")
		} else {
			snapshot.Scans = inventory.Scans
		}
	} else {
		snapshot.Unavailable = append(snapshot.Unavailable, "scan ledger")
	}
	verdicts, err := ReviewVerdicts(ctx, m, run, manifest)
	snapshot.Verdicts = verdicts
	if err != nil {
		snapshot.Unavailable = append(snapshot.Unavailable, "review evidence ledger")
	}
	return snapshot
}
