package workflowadmin

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"github.com/lycaon/lycaon/internal/api/httpio"
	"github.com/lycaon/lycaon/internal/blueprint"
	"github.com/lycaon/lycaon/internal/project"
	"github.com/lycaon/lycaon/internal/workflow"
	wire "github.com/lycaon/lycaon/pkg/api"
)

func (s *BlueprintRoutes) HandleApproveBlueprint(w http.ResponseWriter, r *http.Request) {
	projectID, path, ok := s.blueprintAddress(w, r)
	if !ok {
		return
	}
	var req wire.BlueprintApproveRequest
	if err := httpio.DecodeJSON(w, r, &req); err != nil {
		s.responses.DecodeError(w, r, err)
		return
	}

	rm := s.Workflows
	projectPath := ""
	if p, err := s.Projects.Get(r.Context(), projectID); err == nil {
		projectPath = project.PrimaryRootPath(p)
	}
	_, err := rm.ApprovePlan(r.Context(), projectID, path, projectPath, req.WorkflowRunID, req.ExpectedRevision, req.ContentDigest)
	if err != nil {
		if errors.Is(err, blueprint.ErrNotFound) {
			s.responses.Fail(w, wire.ApiErrorCodeBlueprintNotFound, "blueprint not found")
			return
		}
		if errors.Is(err, workflow.ErrHumanApprovalNotReady) {
			s.responses.Fail(w, wire.ApiErrorCodeHumanApprovalNotReady, "human approval is not ready")
			return
		}
		if errors.Is(err, workflow.ErrRunRevisionConflict) {
			s.responses.Fail(w, wire.ApiErrorCodeWorkflowRevisionConflict, "workflow run changed; reload it")
			return
		}
		if errors.Is(err, workflow.ErrBlueprintApprovalConflict) {
			s.responses.Fail(w, wire.ApiErrorCodeBlueprintContentConflict, "blueprint changed; reload it")
			return
		}
		if errors.Is(err, workflow.ErrNoActiveRun) {
			s.responses.Fail(w, wire.ApiErrorCodeWorkflowRunNotActive, "no active workflow run")
			return
		}
		s.responses.InternalError(w, r, err)
		return
	}
	out, err := s.Blueprints.Get(r.Context(), projectID, path)
	if err != nil {
		s.responses.InternalError(w, r, err)
		return
	}
	_ = rm.SyncBlueprintTranscript(r.Context(), projectID, out.Path, false)
	httpio.WriteJSON(w, http.StatusOK, out)
}

func (s *BlueprintRoutes) HandleLaunchBlueprint(w http.ResponseWriter, r *http.Request) {
	rm := s.Workflows
	projectID, path, ok := s.blueprintAddress(w, r)
	if !ok {
		return
	}
	var req wire.LaunchBlueprintRequest
	if _, err := httpio.DecodeOptionalJSON(w, r, &req); err != nil {
		s.responses.DecodeError(w, r, err)
		return
	}
	source, err := s.Blueprints.Get(r.Context(), projectID, path)
	if err != nil {
		if errors.Is(err, blueprint.ErrNotFound) {
			s.responses.Fail(w, wire.ApiErrorCodeBlueprintNotFound, "blueprint not found")
			return
		}
		s.responses.InternalError(w, r, err)
		return
	}
	sourceContent := source.Content
	sourcePath := source.Path

	p, err := s.Projects.Get(r.Context(), source.ProjectID)
	if err != nil {
		if errors.Is(err, project.ErrNotFound) {
			s.responses.Fail(w, wire.ApiErrorCodeProjectNotFound, "project not found")
			return
		}
		s.responses.InternalError(w, r, err)
		return
	}
	projectDir := project.PrimaryRootPath(p)
	manifests, err := s.blueprintCatalogManifests(r.Context(), projectDir)
	if err != nil {
		s.responses.InternalError(w, r, err)
		return
	}
	target, err := workflow.ResolveLaunchTarget(source.Path, req.TargetWorkflowID, manifests)
	if err != nil {
		s.writeBlueprintLaunchError(w, r, err)
		return
	}

	posture := wire.SessionPostureSpec
	if ip := strings.TrimSpace(target.InitialPosture); ip != "" {
		posture = wire.SessionPosture(ip)
	}
	sess, err := s.SessionAdmin.MaterializeSession(r.Context(), wire.CreateSessionRequest{
		ProjectID: source.ProjectID,
		Posture:   posture,
	})
	if err != nil {
		s.SessionAdmin.WriteSessionCreationError(w, r, err)
		return
	}
	keepSession := false
	cleanupCtx := context.WithoutCancel(r.Context())
	defer func(ctx context.Context) {
		if !keepSession {
			s.SessionAdmin.DiscardMaterializedSession(ctx, sess.ID)
		}
	}(cleanupCtx)
	projectDir = strings.TrimSpace(sess.WorkspacePath)
	if projectDir == "" {
		projectDir = project.PrimaryRootPath(p)
	}

	run, seed, err := rm.LaunchFromBlueprint(r.Context(), sess.ID, source, target, s.Blueprints, projectDir, req.DeferStart)
	if err != nil {
		s.writeBlueprintLaunchError(w, r, err)
		return
	}
	reloaded, err := s.Blueprints.Get(r.Context(), source.ProjectID, sourcePath)
	if err != nil {
		s.responses.InternalError(w, r, err)
		return
	}
	if reloaded.Content != sourceContent {
		s.responses.InternalError(w, r, errors.New("source blueprint mutated during launch"))
		return
	}
	resp := wire.LaunchBlueprintResponse{
		SessionID:   sess.ID,
		BlueprintID: seed.ID,
	}
	if run != nil {
		s.Topology.StartOrchestratedTopologyForRun(r.Context(), sess.ID, run)
		resp.WorkflowRunID = run.ID
	}
	s.SessionAdmin.PublishSessionCreated(r.Context(), sess)
	keepSession = true
	httpio.WriteJSON(w, http.StatusCreated, resp)
}

func (s *BlueprintRoutes) writeBlueprintLaunchError(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, workflow.ErrBlueprintLaunchIncompatible):
		s.responses.Fail(w, wire.ApiErrorCodeBlueprintLaunchIncompatible, "this blueprint cannot launch the selected workflow")
	case errors.Is(err, workflow.ErrBlueprintLaunchUnsupported):
		s.responses.Fail(w, wire.ApiErrorCodeBlueprintLaunchUnsupported, "this workflow cannot launch from a blueprint")
	default:
		s.RunControl.WriteWorkflowError(w, r, err)
	}
}
