package workflow

import (
	"context"

	scanfindings "github.com/lycaon/lycaon/internal/scan/findings"
	workflowdef "github.com/lycaon/lycaon/internal/workflow/definition"
	"github.com/lycaon/lycaon/pkg/api"
)

// candidateInventory never contributes to accepted accounting. Only a decoded,
// structurally valid candidate can supply the separately labeled draft view.
func (m *RunManager) candidateInventory(ctx context.Context, run *api.WorkflowRun, manifest workflowdef.Manifest, inventory RunInventory) (scanfindings.InventoryAccount, string, error) {
	empty := scanfindings.AccountInventory(inventory.Groups, nil, nil)
	vars, err := m.Store.GetScaffoldVars(ctx, run.ID)
	if err != nil {
		return empty, "", err
	}
	repair, err := CurrentReviewRepair(vars, run.CurrentPhase)
	if err != nil {
		return empty, "", err
	}
	if repair == nil || repair.CandidateMessageID == "" || m.Sessions == nil {
		return empty, "absent", nil
	}
	phase, ok := manifest.PhaseByID(run.CurrentPhase)
	if !ok || phase.ReviewLoop == nil {
		return empty, "absent", nil
	}
	messages, err := m.Sessions.GetMessages(ctx, run.SessionID)
	if err != nil {
		return empty, "", err
	}
	for _, msg := range messages {
		if msg.ID != repair.CandidateMessageID || msg.ToolResult == nil {
			continue
		}
		verdict, _, _, err := parseSubmitVerdictArgs(*phase.ReviewLoop, msg.ToolResult.ToolArgs)
		if err != nil {
			return empty, "invalid", nil
		}
		rules, err := m.VerdictRulesFor(ctx, run)
		if err != nil {
			return empty, "", err
		}
		if ValidateReviewLoopVerdict(*phase.ReviewLoop, verdict, rules) != nil {
			return empty, "invalid", nil
		}
		sets, err := ParseVerdictSetAsides(*phase.ReviewLoop, verdict)
		if err != nil {
			return empty, "invalid", nil
		}
		byField, err := ParseVerdictClaims(*phase.ReviewLoop, verdict)
		if err != nil {
			return empty, "invalid", nil
		}
		var linked []string
		for _, claims := range byField {
			for _, claim := range claims {
				linked = append(linked, claim.ScanGroupIDs...)
			}
		}
		account := scanfindings.AccountInventory(inventory.Groups, linked, setAsideSelectors(sets))
		if len(account.Unknown) > 0 {
			return empty, "invalid", nil
		}
		return account, "draft", nil
	}
	return empty, "unavailable", nil
}

func selectorPartlyMatches(selector scanfindings.SetAside, group scanfindings.InventoryGroup) bool {
	if !selector.HasSelector() || selector.Selects(group) {
		return false
	}
	for _, path := range group.Paths {
		one := group
		one.Paths = []string{path}
		if selector.Selects(one) {
			return true
		}
	}
	return false
}
