package delegation

import (
	"context"
	"fmt"
	"strings"

	"github.com/lycaon/lycaon/internal/inspector"
	"github.com/lycaon/lycaon/internal/scan"
	"github.com/lycaon/lycaon/pkg/api"
)

// InspectorCloseoutGate blocks delegation closeout when verify or security gates are unsatisfied.
type InspectorCloseoutGate struct {
	Inspector inspector.Inspector
	Store     Store
	Security  *scan.SecurityCloseoutChecker
}

// Check validates inspector gates for completed legs with verify criteria and security evidence.
func (g *InspectorCloseoutGate) Check(ctx context.Context, delegationID string) error {
	if g == nil || g.Store == nil {
		return nil
	}
	if err := g.checkVerify(ctx, delegationID); err != nil {
		return err
	}
	if g.Security != nil {
		delegation, err := g.Store.Get(ctx, delegationID)
		if err != nil {
			return err
		}
		if err := g.Security.Check(ctx, delegationID, delegation.WorkspacePath); err != nil {
			return err
		}
	}
	return nil
}

func (g *InspectorCloseoutGate) checkVerify(ctx context.Context, delegationID string) error {
	if g.Inspector == nil {
		return nil
	}
	legs, err := g.Store.ListLegs(ctx, delegationID)
	if err != nil {
		return err
	}
	var taskIDs []string
	for _, leg := range legs {
		if leg.Status != api.LegStatusComplete {
			continue
		}
		if needsVerifyEvidence(leg.CompletionCriteria) {
			taskIDs = append(taskIDs, leg.ID)
		}
	}
	if len(taskIDs) == 0 {
		return nil
	}
	result, err := g.Inspector.CheckGates(ctx, delegationID, taskIDs, nil)
	if err != nil {
		return err
	}
	if result != nil && !result.OK {
		return fmt.Errorf("verify gates unsatisfied: missing=%v errors=%v", result.Missing, result.Errors)
	}
	return nil
}

func needsVerifyEvidence(criteria []string) bool {
	for _, c := range criteria {
		if strings.HasPrefix(c, "test:pass:") {
			return true
		}
	}
	return false
}
