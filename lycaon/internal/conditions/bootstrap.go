package conditions

import (
	"context"

	"github.com/lycaon/lycaon/pkg/api"
)

// RegistryDeps supplies optional host services for domain predicates and core wiring.
type RegistryDeps struct {
	DelegationStore         DelegationReader
	Evidence                EvidenceReader
	GroundingBlocked        func(ctx context.Context, sessionID string) (bool, error)
	DoomLoopExceeded        func(ctx context.Context, sessionID string) (bool, error)
	BlueprintGet            func(ctx context.Context, blueprintPath string) (*api.Blueprint, error)
	DelegationCloseout      func(ctx context.Context, sessionID string) (bool, error)
	SourceVerifyPassed      func(ctx context.Context, sessionID string) (bool, error)
	DeliveryReported        func(ctx context.Context, sessionID, runID, phase string) (bool, error)
	ScanLedger              ScanLedger
	SourceSnapshots         SourceSnapshotReader
	ScanProactiveCategories []api.ScanCategory
	SecurityScannersEnabled func() bool
	// ObligationResolvers resolve parameterized obligation gates.
	ObligationResolvers map[string]ObligationStatusReader
	ApprovalDenied      func(ctx context.Context, sessionID string) (bool, error)
	// Delegate/subroutine gates.
	WorkerCycleIdle  func(projectID, sessionID, completingJobID string) (bool, error)
	ChildRunStatus   ChildRunStatusReader
	BlueprintContent func(ctx context.Context, projectDir, relPath string) (string, error)
}

// NewDefaultRegistry registers bundled workflow and rule conditions.
func NewDefaultRegistry(deps RegistryDeps) (*ConditionRegistry, error) {
	reg := NewRegistry()
	core := CoreDeps{
		DelegationStore:         deps.DelegationStore,
		Evidence:                deps.Evidence,
		DelegationCloseout:      deps.DelegationCloseout,
		SourceVerifyPassed:      deps.SourceVerifyPassed,
		DeliveryReported:        deps.DeliveryReported,
		ScanLedger:              deps.ScanLedger,
		SourceSnapshots:         deps.SourceSnapshots,
		ScanProactiveCategories: deps.ScanProactiveCategories,
		SecurityScannersEnabled: deps.SecurityScannersEnabled,
		GroundingBlocked:        deps.GroundingBlocked,
		DoomLoopExceeded:        deps.DoomLoopExceeded,
		ApprovalDenied:          deps.ApprovalDenied,
	}
	if err := RegisterCoreConditions(reg, core); err != nil {
		return nil, err
	}
	if err := RegisterPlanDomain(reg, deps); err != nil {
		return nil, err
	}
	if err := RegisterBlueprintDomain(reg, deps); err != nil {
		return nil, err
	}
	if err := RegisterScanDomain(reg, deps); err != nil {
		return nil, err
	}
	if err := RegisterObligationDomain(reg, deps.ObligationResolvers); err != nil {
		return nil, err
	}
	if err := RegisterDelegateDomain(reg, deps); err != nil {
		return nil, err
	}
	if err := RegisterHumanApprovalDomain(reg, deps); err != nil {
		return nil, err
	}
	if err := RegisterProjectConditions(reg); err != nil {
		return nil, err
	}
	if err := RegisterProjectToolConditions(reg); err != nil {
		return nil, err
	}
	return reg, nil
}

// TestRegistryDeps wires shipped predicates for unit tests with no active delegation.
func TestRegistryDeps() RegistryDeps {
	return RegistryDeps{
		BlueprintGet: func(_ context.Context, blueprintPath string) (*api.Blueprint, error) {
			return &api.Blueprint{Path: blueprintPath, Content: TestPlanContentWithTasks}, nil
		},
		DelegationCloseout: func(context.Context, string) (bool, error) {
			return true, nil
		},
	}
}
