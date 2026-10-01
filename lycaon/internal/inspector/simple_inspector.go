package inspector

import (
	"context"
	"fmt"

	"github.com/lycaon/lycaon/internal/evidence"
)

// SimpleInspector records JSONL evidence and evaluates verify gates from disk.
type SimpleInspector struct {
	Store      EvidenceStore
	Root       string
	Required   []evidence.GateType
	ProjectDir RunProjectDir
}

// NewSimpleInspector constructs an inspector with default verify gate types.
func NewSimpleInspector(store EvidenceStore) *SimpleInspector {
	if store == nil {
		store = NewJSONLStore(DefaultEvidenceDir)
	}
	return &SimpleInspector{
		Store: store,
		Root:  DefaultEvidenceDir,
		Required: []evidence.GateType{
			evidence.GateTypeVerify,
		},
	}
}

// RecordEvidence appends one evidence record.
func (i *SimpleInspector) RecordEvidence(ctx context.Context, runID, slot string, rec evidence.Record) error {
	if i == nil || i.Store == nil {
		return fmt.Errorf("inspector store not configured")
	}
	rec.RunID = runID
	rec.Slot = slot
	projectDir, err := i.projectDir(ctx, runID)
	if err != nil {
		return err
	}
	return i.Store.Append(ctx, projectDir, rec)
}

// CheckGates verifies latest anchored evidence for each task.
func (i *SimpleInspector) CheckGates(ctx context.Context, runID string, slots []string, _ []string) (*GateCheckResult, error) {
	if i == nil {
		return &GateCheckResult{OK: true}, nil
	}
	projectDir, err := i.projectDir(ctx, runID)
	if err != nil {
		return nil, err
	}
	result := &GateCheckResult{OK: true}
	for _, slot := range slots {
		for _, gateType := range i.Required {
			records, err := i.Store.ReadAll(ctx, projectDir, runID, slot, gateType)
			if err != nil {
				return nil, err
			}
			latest := Latest(records)
			if latest == nil {
				result.OK = false
				result.Missing = append(result.Missing, formatMissing(runID, slot, gateType))
				continue
			}
			ok, reason := EvidenceAnchored(*latest)
			if !ok {
				result.OK = false
				result.Errors = append(result.Errors, fmt.Sprintf("%s: %s", formatMissing(runID, slot, gateType), reason))
			}
		}
	}
	return result, nil
}

// GetStatus summarizes gate evidence for one task.
func (i *SimpleInspector) GetStatus(ctx context.Context, runID, slot string) (*GateStatus, error) {
	projectDir, err := i.projectDir(ctx, runID)
	if err != nil {
		return nil, err
	}
	status := &GateStatus{
		Slot:          slot,
		RunID:         runID,
		RequiredGates: append([]evidence.GateType(nil), i.Required...),
		Evidence:      map[evidence.GateType]*evidence.Record{},
		Satisfied:     true,
	}
	for _, gateType := range i.Required {
		records, err := i.Store.ReadAll(ctx, projectDir, runID, slot, gateType)
		if err != nil {
			return nil, err
		}
		latest := Latest(records)
		if latest != nil {
			status.Evidence[gateType] = latest
		}
		ok, _ := EvidenceAnchored(deref(latest))
		if latest == nil || !ok {
			status.Satisfied = false
		}
	}
	return status, nil
}

// RunProjectDir resolves project directory for a delegation.
type RunProjectDir func(ctx context.Context, runID string) (string, error)

func deref(rec *evidence.Record) evidence.Record {
	if rec == nil {
		return evidence.Record{}
	}
	return *rec
}

func (i *SimpleInspector) projectDir(ctx context.Context, runID string) (string, error) {
	if i != nil && i.ProjectDir != nil {
		return i.ProjectDir(ctx, runID)
	}
	return "", fmt.Errorf("delegation project dir lookup not configured")
}

var _ Inspector = (*SimpleInspector)(nil)
