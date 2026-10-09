package assembly

import (
	"context"
	"strings"

	"github.com/lycaon/lycaon/internal/coordinator/inject"
	"github.com/lycaon/lycaon/pkg/api"
)

func (e *turnContextAssembler) skillProcedureInject(ctx context.Context, sess *api.Session, profileID string) (api.Message, bool) {
	deps := e.deps
	if deps.SkillPreload == nil || deps.Injects == nil || sess == nil {
		return api.Message{}, false
	}
	preload := deps.SkillPreload(sess.ID)
	if preload == nil || strings.TrimSpace(preload.Body) == "" {
		return api.Message{}, false
	}
	block, err := inject.RenderSkillProcedureBlock(ctx, deps.Injects, sess.ID, profileID, *preload)
	if err != nil || block == "" {
		return api.Message{}, false
	}
	return api.Message{ID: "host-skill-procedure", Role: api.MessageRoleSystem, Content: block,
		Origin: api.MessageOriginHost, Authority: api.ContentAuthoritySystem, TrustTier: api.ContentTrustTierTrusted, ContextPinned: true}, true
}

// skillPointerInject names, in one line, the skill a turn's first loadable
// tool call fitted without reading it. A read skill supersedes the pointer.
func (e *turnContextAssembler) skillPointerInject(ctx context.Context, sess *api.Session, profileID string) (api.Message, bool) {
	deps := e.deps
	if deps.SkillPointer == nil || deps.Injects == nil || sess == nil {
		return api.Message{}, false
	}
	if deps.SkillPreload != nil && deps.SkillPreload(sess.ID) != nil {
		return api.Message{}, false
	}
	pointer := deps.SkillPointer(sess.ID)
	if pointer == nil {
		return api.Message{}, false
	}
	block, err := inject.RenderSkillPointerBlock(ctx, deps.Injects, sess.ID, profileID, *pointer)
	if err != nil || block == "" {
		return api.Message{}, false
	}
	return api.Message{ID: "host-skill-pointer", Role: api.MessageRoleSystem, Content: block,
		Origin: api.MessageOriginHost, Authority: api.ContentAuthoritySystem, TrustTier: api.ContentTrustTierTrusted, ContextPinned: true}, true
}
