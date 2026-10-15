package providerwire

import (
	"context"

	"github.com/lycaon/lycaon/internal/llm/modelcall"
	"github.com/lycaon/lycaon/internal/llm/modelinfo"
	"github.com/lycaon/lycaon/pkg/api"
)

// CatalogRequestBudget counts only images eligible for this dispatch.
func CatalogRequestBudget(ctx context.Context, req modelcall.CompletionRequest, catalog modelcall.Catalog) modelcall.RequestBudget {
	entry, _ := catalog.ModelEntry(catalog.ResolveModel(req))
	vision := modelinfo.Supported(entry.EffectiveCapabilities().Vision)
	prepared := PrepareMessagesForVision(req.Messages, vision, req.Debug.SessionID)
	req.Messages = append([]api.Message(nil), prepared...)
	for i, row := range req.Messages {
		req.Messages[i].ArtifactIDs = nil
		if !vision || row.Role != api.MessageRoleUser {
			continue
		}
		for _, id := range row.ArtifactIDs {
			if _, _, ok := UserArtifactWireImage(req.Debug.SessionID, id); ok {
				req.Messages[i].ArtifactIDs = append(req.Messages[i].ArtifactIDs, id)
			}
		}
	}
	return catalog.RequestBudget(ctx, req)
}
