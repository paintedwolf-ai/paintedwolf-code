package assembly

import (
	"context"

	"github.com/lycaon/lycaon/pkg/api"
)

func (e *turnContextAssembler) prependSynthesisEvidenceInject(
	ctx context.Context,
	sess *api.Session,
	surfaceID string,
) []api.Message {
	if e == nil || sess == nil || sess.IsWorkerChild() {
		return nil
	}
	src := e.surface.wiring.SynthesisEvidence
	if src == nil {
		return nil
	}
	block := src.SynthesisEvidenceForAssembly(ctx, sess, surfaceID)
	if block == "" {
		return nil
	}
	return []api.Message{{
		Role: api.MessageRoleSystem, Content: block,
		Origin: api.MessageOriginHost, Authority: api.ContentAuthorityNone, TrustTier: api.ContentTrustTierTrusted,
	}}
}
