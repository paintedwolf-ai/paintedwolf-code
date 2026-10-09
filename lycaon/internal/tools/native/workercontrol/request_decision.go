package workercontrol

import (
	"context"
	"fmt"
	"github.com/lycaon/lycaon/internal/toolrejection"
	"strings"

	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/tools/surveyjson"
	"github.com/lycaon/lycaon/internal/visual"
	"github.com/lycaon/lycaon/pkg/api"
)

// DecisionRecorder persists a worker's pending decision.
type DecisionRecorder interface {
	Put(context.Context, api.WorkerDecisionRequest) error
}

// RequestDecisionDeps wires request_decision, including optional artifact attachment.
type RequestDecisionDeps struct {
	Recorder      DecisionRecorder
	Artifacts     visual.Store
	RootSessionID func(ctx context.Context, sessionID string) string
}

// RequestDecisionTool is the worker decision-request tool name.
const RequestDecisionTool = "request_decision"

func stringArg(args map[string]any, key string) string {
	value, _ := args[key].(string)
	return value
}

func DecisionHandler(deps RequestDecisionDeps) tools.ToolHandler {
	recorder := deps.Recorder
	artifacts := deps.Artifacts
	rootOf := deps.RootSessionID
	return func(ctx context.Context, args map[string]any, tctx tools.ToolContext) (string, error) {
		if tools.OutOfSessionScope(RequestDecisionTool, tctx) {
			return "", &toolrejection.ToolReject{
				Code: "REQUEST_DECISION_ADDRESSED_SESSION",
				Data: map[string]any{"tool": RequestDecisionTool},
			}
		}
		child := strings.TrimSpace(tctx.SessionID)
		if child == "" {
			return "", fmt.Errorf("session required")
		}
		question, _ := args["question"].(string)
		question = strings.TrimSpace(question)
		if question == "" {
			return "", fmt.Errorf("question is required")
		}
		var options []string
		if raw, ok := args["options"].([]any); ok {
			for _, it := range raw {
				if s, ok := it.(string); ok && strings.TrimSpace(s) != "" {
					options = append(options, strings.TrimSpace(s))
				}
			}
		}
		if len(options) < 2 {
			return "", fmt.Errorf("provide at least 2 concrete options for the coordinator to choose from")
		}
		blockerClass := api.WorkerBlockerClass(strings.TrimSpace(stringArg(args, "blocker_class")))
		if blockerClass == "" {
			blockerClass = api.WorkerBlockerDecision
		}
		switch blockerClass {
		case api.WorkerBlockerDecision, api.WorkerBlockerCheckpoint, api.WorkerBlockerSandbox:
		default:
			return "", fmt.Errorf("blocker_class must be decision, checkpoint, or sandbox")
		}
		attachment, err := parseDecisionAttachment(args)
		if err != nil {
			return "", err
		}
		artifactID, artifactIDs := attachment.Single, attachment.Compare
		root := child
		if rootOf != nil {
			if r := strings.TrimSpace(rootOf(ctx, child)); r != "" {
				root = r
			}
		}
		validateID := func(id string) error {
			res := visual.ResolveInTree(ctx, artifacts, root, id)
			switch {
			case res.IsPresent():
				if !visual.IsInteractivePreviewMime(res.Meta().Mime) {
					return &toolrejection.ToolReject{Code: "REQUEST_DECISION_ARTIFACT_UNSUPPORTED", Data: map[string]any{
						"artifact_id": id, "mime": res.Meta().Mime, "artifact_preview_mimes": visual.InteractivePreviewMIMEs(),
					}}
				}
				return nil
			case res.Reason() == visual.AbsenceForeign:
				return &toolrejection.ToolReject{Code: "REQUEST_DECISION_ARTIFACT_FOREIGN", Data: map[string]any{"artifact_id": id}}
			default:
				return &toolrejection.ToolReject{Code: "REQUEST_DECISION_ARTIFACT_NOT_FOUND", Data: map[string]any{"artifact_id": id, "reason": string(res.Reason())}}
			}
		}
		if artifactID != "" {
			if err := validateID(artifactID); err != nil {
				return "", err
			}
		}
		for _, id := range artifactIDs {
			if err := validateID(id); err != nil {
				return "", err
			}
		}
		jobID := strings.TrimSpace(tctx.WorkerJobID)
		if jobID == "" {
			return "", fmt.Errorf("request_decision requires an active worker job")
		}
		if recorder == nil {
			return "", fmt.Errorf("decision store required")
		}
		if err := recorder.Put(ctx, api.WorkerDecisionRequest{
			ChildSessionID: child, WorkerID: jobID, Question: question, Options: options, BlockerClass: blockerClass,
			ArtifactID: artifactID, ArtifactIDs: artifactIDs,
		}); err != nil {
			return "", fmt.Errorf("store decision: %w", err)
		}
		body := map[string]any{
			"status":           "decision_requested",
			"job_id":           jobID,
			"child_session_id": child,
			"question":         question,
			"options":          options,
			"blocker_class":    blockerClass,
		}
		if artifactID != "" {
			body["artifact_id"] = artifactID
		}
		if len(artifactIDs) > 0 {
			body["artifact_ids"] = artifactIDs
		}
		raw, _ := surveyjson.Marshal(body)
		return string(raw), nil
	}
}
