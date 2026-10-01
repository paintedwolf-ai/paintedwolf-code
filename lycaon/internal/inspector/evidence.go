package inspector

import (
	"context"

	"github.com/lycaon/lycaon/internal/evidence"
)

// DefaultEvidenceDir is the subdirectory under a project's host data dir for evidence JSONL.
const DefaultEvidenceDir = "evidence"

// EvidencePath returns the JSONL path for a gate type.
// Layout: {root}/{run_id}/{slot}/{type}.jsonl
func EvidencePath(root, runID, slot string, gateType evidence.GateType) string {
	return root + "/" + runID + "/" + slot + "/" + string(gateType) + ".jsonl"
}

// EvidenceStore appends evidence records to JSONL files on disk.
type EvidenceStore interface {
	Append(ctx context.Context, projectDir string, record evidence.Record) error
	ReadAll(ctx context.Context, projectDir, runID, slot string, gateType evidence.GateType) ([]evidence.Record, error)
}
