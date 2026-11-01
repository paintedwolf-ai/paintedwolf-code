package promptloop

import (
	"context"
	"fmt"
	"strings"

	"github.com/lycaon/lycaon/internal/coordinator/inject"
	"github.com/lycaon/lycaon/internal/llm/compaction"
	"github.com/lycaon/lycaon/internal/llm/modelcall"
	"github.com/lycaon/lycaon/internal/oar"
	"github.com/lycaon/lycaon/internal/prompts"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/toolsurface"
	"github.com/lycaon/lycaon/pkg/api"
)

// coordinatorTurnRequest is one assembled provider call.
type coordinatorTurnRequest struct {
	Req modelcall.CompletionRequest
	// SurfaceID is empty outside coordinator profiles.
	SurfaceID string
	// TranscriptEstimate is the pre-call token estimate.
	TranscriptEstimate int
	ToolPlan           toolsurface.Plan
}

// buildTurnRequest assembles and detaches one provider request.
func (l *modelTurn) buildTurnRequest(
	ctx context.Context,
	sess *api.Session,
	sessionID string,
	history []api.Message,
	profileID string,
	userPrompt string,
	iterIndex, maxIter int,
	hostTurn bool,
	st *promptLoopTurnState,
) (coordinatorTurnRequest, error) {
	if l.Context.Deps.BuildMessages == nil {
		return coordinatorTurnRequest{}, fmt.Errorf("build messages not configured")
	}
	frameValue := inject.CoordinatorTurnFrame{}
	var frame *inject.CoordinatorTurnFrame
	if st != nil && st.coordinatorFrameReady {
		frame = &st.coordinatorFrame
		frameValue = st.coordinatorFrame
	}
	messages, err := l.Context.Deps.BuildMessages(ctx, sess, history, frame)
	if err != nil {
		return coordinatorTurnRequest{}, err
	}
	if frame != nil {
		frameValue = *frame
	}
	if st != nil && st.proseTurn(sess, iterIndex, maxIter) {
		st.proseFinish = true
	}
	turnTools, toolPlan, surfaceID, err := l.Context.coordinatorToolsForTurn(
		ctx, sess, profileID, history, userPrompt, iterIndex, maxIter, frameValue, st,
	)
	if err != nil {
		return coordinatorTurnRequest{}, err
	}
	if st != nil && st.spendSoftStopGranted {
		turnTools = filterSpendSoftStopToolMetas(turnTools)
	}
	// A final turn with no completion tool keeps its tool definitions, which
	// transports validate tool history against, and forbids calling them.
	// Closeout repairs on earlier turns keep the surface callable: the repair
	// kick offers resuming work, which retires the pinned report.
	toolUse := modelcall.ToolUseAllowed
	callable := turnTools
	if st != nil && st.proseFinish && len(workerProseOfferedTools(sess)) == 0 {
		toolUse = modelcall.ToolUseForbidden
		callable = []tools.ToolMeta{}
	}
	procedureProfile := profileID
	if surfaceID != "" {
		procedureProfile = surfaceID
	}
	messages, err = l.Tools.appendToolProcedures(ctx, sess, procedureProfile, messages, callable)
	if err != nil {
		return coordinatorTurnRequest{}, err
	}
	if reject, blocked, _, changed := l.Closeout.evaluateContentAnchor(ctx, sess, oar.AnchorContentInput, oar.ContentObservation{Segments: oarContentSegments(messages)}, "", nil); blocked {
		return coordinatorTurnRequest{}, reject
	} else if changed {
		return coordinatorTurnRequest{}, fmt.Errorf("[OAR-PROF-10] model.input transform cannot preserve message roles and provenance")
	}
	if l.Inbox.Deps.TakePolicyFeedback != nil {
		feedback, err := l.Inbox.Deps.TakePolicyFeedback(ctx, sessionID)
		if err != nil {
			return coordinatorTurnRequest{}, err
		}
		messages = append(messages, feedback...)
		if st != nil {
			st.history = append(st.history, feedback...)
		}
	}
	// Detach the request from mutable history.
	messages = append([]api.Message(nil), messages...)
	req := modelcall.CompletionRequest{
		Messages: messages,
		Debug: modelcall.RequestDebug{
			SessionID:        sessionID,
			ProjectID:        sess.ProjectID,
			ProjectDir:       l.Context.primaryProjectRoot(ctx, sess),
			RootSessionID:    rootModelRequestSessionID(sess, sessionID),
			AgentType:        sess.AgentType,
			ParentSessionID:  sess.ParentSessionID,
			ProfileID:        profileID,
			HostTurn:         hostTurn,
			Surface:          surfaceID,
			Iteration:        currentModelIteration(st, iterIndex),
			MaxIterations:    maxIter,
			WorkflowRevision: frameValue.WorkflowRevision,
			CommandGuidance:  l.buildCommandGuidanceDebug(ctx, sess, profileID, surfaceID, frameValue, turnTools),
		},
	}
	if err := validateTurnToolFunctionRoots(turnTools); err != nil {
		return coordinatorTurnRequest{}, err
	}
	req.Tools = turnTools
	req.ToolUse = toolUse
	var policyBinding tools.AgentPolicyBinding
	if l.Context.Deps.PrepareAgentPolicy != nil {
		var bindErr error
		policyBinding, bindErr = l.Context.Deps.PrepareAgentPolicy(ctx, sess, req.Messages)
		if bindErr != nil {
			return coordinatorTurnRequest{}, bindErr
		}
	}
	if l.Deps.FitRequest != nil {
		req, err = l.Deps.FitRequest(ctx, sess, history, req)
		if err != nil {
			return coordinatorTurnRequest{}, err
		}
	}
	if st != nil {
		st.output.policyCheck = nil
		if policyBinding != nil {
			st.output.policyCheck = policyBinding(req.Messages)
		}
	} else if policyBinding != nil {
		return coordinatorTurnRequest{}, fmt.Errorf("model request policy requires turn state")
	}
	st.stampOfferedToolNames(callable)
	return coordinatorTurnRequest{
		Req:                req,
		SurfaceID:          surfaceID,
		TranscriptEstimate: compaction.EstimateMessagesTokens(compaction.ContextMessagesFromAPI(req.Messages)),
		ToolPlan:           toolPlan,
	}, nil
}

func validateTurnToolFunctionRoots(metas []tools.ToolMeta) error {
	for _, meta := range metas {
		if meta.ArgsSchema == nil {
			continue
		}
		if err := tools.ValidateFunctionParametersRoot(meta.ArgsSchema); err != nil {
			name := strings.TrimSpace(meta.Name)
			if name == "" {
				name = "unnamed"
			}
			return fmt.Errorf("tool %q: %w", name, err)
		}
	}
	return nil
}

// primaryProjectRoot keeps project leases stable across worker checkouts.
func (l *promptContext) primaryProjectRoot(ctx context.Context, sess *api.Session) string {
	if l == nil || sess == nil {
		return ""
	}
	return strings.TrimSpace(l.citationRoots(ctx, sess).ProjectDir)
}

func (l *modelTurn) buildCommandGuidanceDebug(
	ctx context.Context,
	sess *api.Session,
	profileID, surfaceID string,
	frame inject.CoordinatorTurnFrame,
	turnTools []tools.ToolMeta,
) *modelcall.CommandGuidanceDebug {
	if sess == nil {
		return nil
	}
	var offered, deferred []string
	for _, meta := range turnTools {
		name := strings.TrimSpace(meta.Name)
		if name == "" {
			continue
		}
		if meta.Deferred {
			deferred = append(deferred, name)
		} else {
			offered = append(offered, name)
		}
	}
	rootCount := 0
	if l != nil && l.Context != nil && l.Context.Deps.ProjectRootCount != nil {
		rootCount = l.Context.Deps.ProjectRootCount(ctx, sess)
	}
	projectDir := ""
	if l != nil && l.Context != nil {
		projectDir = l.Context.primaryProjectRoot(ctx, sess)
	}
	templates, facts := prompts.BuildCommandGuidanceSnapshot(prompts.CommandGuidanceSnapshotInput{
		ProfileID:             profileID,
		SurfaceID:             surfaceID,
		AgentType:             sess.AgentType,
		IsCoordinator:         profileID == prompts.CoordinatorProfileID || strings.TrimSpace(surfaceID) != "",
		OfferedTools:          offered,
		DeferredTools:         deferred,
		RootCount:             rootCount,
		ProjectDir:            projectDir,
		SessionScratchDir:     frame.Machine.Surface.SessionScratchDir,
		Posture:               frame.Machine.Surface.Posture,
		CommandExternalHidden: frame.Machine.Surface.CommandExternalHidden,
	})
	if len(templates) == 0 && len(facts) == 0 {
		return nil
	}
	return &modelcall.CommandGuidanceDebug{
		Templates: templates,
		Facts:     facts,
	}
}
