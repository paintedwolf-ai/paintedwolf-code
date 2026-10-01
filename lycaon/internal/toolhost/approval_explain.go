package toolhost

import (
	"github.com/lycaon/lycaon/internal/approvals"
	"github.com/lycaon/lycaon/internal/hitl"
	"github.com/lycaon/lycaon/internal/settings"
	"github.com/lycaon/lycaon/internal/tools"
)

type registryExplainer struct {
	reg *approvals.Registry
}

func (r *registryExplainer) ExplainApproval(action hitl.ProposedAction) tools.ApprovalExplanation {
	tier := settings.ClassifyTier(action)
	copy := r.reg.ExplainAction(action, tier).Copy
	return tools.ApprovalExplanation{
		What:      copy.What,
		Who:       copy.Who,
		IfWrong:   copy.IfWrong,
		AllowLine: copy.AllowLine,
	}
}

func newRegistryExplainer(reg *approvals.Registry) tools.ApprovalExplainer {
	if reg == nil {
		return nil
	}
	return &registryExplainer{reg: reg}
}
