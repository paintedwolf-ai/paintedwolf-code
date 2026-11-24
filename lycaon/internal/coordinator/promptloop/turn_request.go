package promptloop

import (
	"context"
	"fmt"
	"log/slog"
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
		st.coordinatorFrame = frameValue
	}
	if st != nil && st.proseTurn(sess, iterIndex, maxIter) {
		st.proseFinish = true
	}
	projection, err := l.Context.coordinatorToolsForTurn(
		ctx, sess, profileID, history, iterIndex, maxIter, frameValue, st,
	)
	if err != nil {
		return coordinatorTurnRequest{}, err
	}
	turnTools, toolPlan, surfaceID := projection.Metas, projection.Plan, projection.SurfaceID
	if st != nil && st.spendSoftStopGranted {
		turnTools = filterSpendSoftStopToolMetas(turnTools)
	}
	// A final turn with no completion tool forbids calling tools. The provider
	// drops the definitions where its API allows that, and otherwise keeps them
	// with its no-call control.
	// Closeout repairs on earlier turns keep the surface callable: the repair
	// kick offers resuming work, which retires the pinned report.
	toolUse := modelcall.ToolUseAllowed
	callable := turnTools
	if st != nil && st.proseFinish && len(workerProseOfferedTools(sess)) == 0 {
		toolUse = modelcall.ToolUseForbidden
		callable = []tools.ToolMeta{}
	}
	reader, readerErr := l.Context.requestReader(ctx, sess, projection, callable)
	if readerErr != nil {
		slog.WarnContext(ctx, "request reader unavailable; reader-gated copy omitted", "session_id", sessionID, "error", readerErr)
	} else {
		messages, err = l.Tools.appendToolProcedures(ctx, sess, reader, messages)
		if err != nil {
			return coordinatorTurnRequest{}, err
		}
	}
	if st != nil {
		st.reader = reader
	}
	if reject, blocked := l.Closeout.evaluateContentAnchor(ctx, sess, oar.AnchorContentInput, oar.ContentObservation{Segments: oarContentSegments(messages)}, "", nil); blocked {
		return coordinatorTurnRequest{}, reject
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
			SessionID:         sessionID,
			ProjectID:         sess.ProjectID,
			ProjectDir:        l.Context.primaryProjectRoot(ctx, sess),
			RootSessionID:     rootModelRequestSessionID(sess, sessionID),
			AgentType:         sess.AgentType,
			ParentSessionID:   sess.ParentSessionID,
			ProfileID:         profileID,
			HostTurn:          hostTurn,
			Surface:           surfaceID,
			Iteration:         currentModelIteration(st, iterIndex),
			MaxIterations:     maxIter,
			WorkflowRevision:  frameValue.WorkflowRevision,
			CommandGuidance:   l.buildCommandGuidanceDebug(ctx, sess, reader, frameValue),
			ReaderFingerprint: reader.Fingerprint(),
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
	st.stampTurnToolSchemas(callable, projection.DeferredSchemas)
	return coordinatorTurnRequest{
		Req:                req,
		SurfaceID:          surfaceID,
		TranscriptEstimate: compaction.EstimateMessagesTokens(compaction.ContextMessagesFromAPI(req.Messages)),
		ToolPlan:           toolPlan,
	}, nil
}

func (st *promptLoopTurnState) stampTurnToolSchemas(callable []tools.ToolMeta, deferred map[string]map[string]any) {
	st.stampOfferedToolNames(callable)
	if st == nil {
		return
	}
	for name, schema := range deferred {
		if _, already := st.offeredToolSchemas[name]; !already {
			st.offeredToolSchemas[name] = schema
		}
	}
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
	reader prompts.Reader,
	frame inject.CoordinatorTurnFrame,
) *modelcall.CommandGuidanceDebug {
	if sess == nil || !reader.Valid() {
		return nil
	}
	projectDir := ""
	if l != nil && l.Context != nil {
		projectDir = l.Context.primaryProjectRoot(ctx, sess)
	}
	templates, facts := prompts.BuildCommandGuidanceSnapshot(reader, prompts.CommandGuidanceSnapshotInput{
		AgentType:             sess.AgentType,
		ProjectDir:            projectDir,
		SessionScratchDir:     frame.Machine.Surface.SessionScratchDir,
		Posture:               frame.Machine.Surface.Posture,
		SharedTempLocations:   frame.Machine.Surface.SharedTempLocations,
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
