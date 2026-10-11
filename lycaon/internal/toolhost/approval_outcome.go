package toolhost

import (
	"github.com/lycaon/lycaon/internal/approvaloutcome"
	"github.com/lycaon/lycaon/internal/toolexecution"
)

type catalogOutcomeRenderer struct {
	cat *approvaloutcome.Catalog
}

func (r *catalogOutcomeRenderer) ApprovalOutcome(code string, ctx map[string]any) string {
	return r.cat.Message(code, ctx)
}

func newCatalogOutcomeRenderer(cat *approvaloutcome.Catalog) toolexecution.ApprovalOutcomeRenderer {
	if cat == nil {
		return nil
	}
	return &catalogOutcomeRenderer{cat: cat}
}
