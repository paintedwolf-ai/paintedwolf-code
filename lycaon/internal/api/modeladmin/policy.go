package modeladmin

import (
	"context"
	"errors"
	"net/http"

	"github.com/lycaon/lycaon/internal/api/httpio"
	"github.com/lycaon/lycaon/internal/api/requestscope"
	"github.com/lycaon/lycaon/internal/events"
	"github.com/lycaon/lycaon/internal/llm"
	"github.com/lycaon/lycaon/internal/project"
	wire "github.com/lycaon/lycaon/pkg/api"
)

func (s *Handler) GetPolicy(w http.ResponseWriter, r *http.Request) {
	scope, ref, err := requestscope.Settings(r, s.projects)
	projectDir := ref.Dir
	if err != nil {
		requestscope.ScopeError(s.responses, w, r, err)
		return
	}
	effective, _, queryErr := httpio.OptionalBoolQuery(r, "effective")
	if queryErr != nil {
		s.responses.InvalidQuery(w, queryErr)
		return
	}
	var p llm.ModelPolicy
	if effective {
		p, err = s.service.Policy.Get(scope, projectDir)
	} else {
		p, err = s.service.Policy.Overlay(scope, projectDir)
	}
	if err != nil {
		s.responses.InternalError(w, r, err)
		return
	}
	httpio.WriteJSON(w, http.StatusOK, llm.ModelPolicyToDTO(p))
}

func (s *Handler) UpdatePolicy(w http.ResponseWriter, r *http.Request) {
	scope, ref, err := requestscope.Settings(r, s.projects)
	projectDir := ref.Dir
	if err != nil {
		requestscope.ScopeError(s.responses, w, r, err)
		return
	}
	var raw map[string]any
	var req wire.ModelPolicyPatch
	if err := httpio.DecodeJSONWithRaw(w, r, &req, &raw); err != nil {
		s.responses.DecodeError(w, r, err)
		return
	}
	if !s.responses.RequireJSONObjectAnyKey(w, raw, "coordinator", "lite", "agent_pool", "thinking_overrides") {
		return
	}
	patch := llm.ModelPolicyPatchFromDTO(req)
	// A null slot clears this layer's assignment so it inherits.
	if value, set := raw["coordinator"]; set && value == nil {
		patch.Coordinator = &llm.ModelRef{}
	}
	if value, set := raw["lite"]; set && value == nil {
		patch.Lite = &llm.ModelRef{}
	}
	if err := s.service.ApplyModelPolicy(r.Context(), scope, projectDir, patch); err != nil {
		var validation *llm.PolicyValidationError
		if errors.As(err, &validation) {
			s.responses.ModelAssignmentError(w, r, err)
			return
		}
		s.responses.InternalError(w, r, err)
		return
	}
	s.publishModelPolicyEvent(r.Context(), string(scope), projectDir)
	overlay, err := s.service.Policy.Overlay(scope, projectDir)
	if err != nil {
		s.responses.InternalError(w, r, err)
		return
	}
	httpio.WriteJSON(w, http.StatusOK, llm.ModelPolicyToDTO(overlay))
}

func (s *Handler) publishModelPolicyEvent(ctx context.Context, scope, projectDir string) {
	key := events.PublishKeyFor(ctx, project.ScopeLookup{Registry: s.projects}, projectDir, "")
	_ = s.events.Publish(ctx, wire.EventTopicModelPolicy, key, wire.ModelPolicyEvent{
		Scope:  scope,
		Action: "updated",
	})
}
