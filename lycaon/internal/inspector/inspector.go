// Package inspector defines evidence gates and persistence.
package inspector

import (
	"context"

	"github.com/lycaon/lycaon/internal/evidence"
)

// GateStatus summarizes gate satisfaction for a run slot.
type GateStatus struct {
	Slot          string
	RunID         string
	RequiredGates []evidence.GateType
	Evidence      map[evidence.GateType]*evidence.Record
	Satisfied     bool
}

// GateCheckResult is the outcome of a gate check.
type GateCheckResult struct {
	OK      bool
	Missing []string
	Errors  []string
}

// Inspector records evidence and evaluates inspector gates.
type Inspector interface {
	RecordEvidence(ctx context.Context, runID, slot string, rec evidence.Record) error
	CheckGates(ctx context.Context, runID string, slots []string, filesTouched []string) (*GateCheckResult, error)
	GetStatus(ctx context.Context, runID, slot string) (*GateStatus, error)
}
