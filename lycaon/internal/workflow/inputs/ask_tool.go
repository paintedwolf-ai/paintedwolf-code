package inputs

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/lycaon/lycaon/internal/toolrejection"
	"github.com/lycaon/lycaon/internal/workflow/toolguard"
	"math"
	"strings"

	"github.com/lycaon/lycaon/internal/sandbox"
	"github.com/lycaon/lycaon/internal/tools"
	workflowdef "github.com/lycaon/lycaon/internal/workflow/definition"
	"github.com/lycaon/lycaon/internal/workflow/runstate"
)

// RegisterAskUserTool registers coordinator-only ask_user. Workspace image
// paths resolve through boundary under the tool read floor.
func RegisterAskUserTool(reg *tools.DefaultRegistry, runs *Asks, boundary *sandbox.Boundary) error {
	if reg == nil {
		return fmt.Errorf("registry required")
	}
	if runs == nil {
		return fmt.Errorf("workflow run manager required")
	}
	return reg.Register("ask_user", func(ctx context.Context, args map[string]any, tctx tools.ToolContext) (string, error) {
		if !toolguard.IsCoordinatorAgent(tctx.Identity.Agent) {
			return "", fmt.Errorf("ask_user requires coordinator role")
		}
		req, err := parseAskUserArgs(args)
		if err != nil {
			return "", err
		}
		req.ToolCallID = strings.TrimSpace(tctx.Identity.ToolCallID)
		req.WorkspaceImages = WorkspaceImageReaderForBoundary(boundary, tctx)
		handle, err := runs.RequestUserInput(ctx, tctx.Identity.SessionID, req)
		if err != nil {
			reject := &AskUserReject{}
			if errors.As(err, &reject) {
				return "", &toolrejection.ToolReject{Code: reject.Code, Data: reject.Data}
			}
			return "", err
		}
		body, err := json.Marshal(map[string]any{
			"status":          "pending",
			"phase_id":        handle.PhaseID,
			"run_id":          handle.RunID,
			"issued_revision": handle.IssuedRevision,
			"prompt":          handle.Prompt,
			"response_type":   string(handle.ResponseType),
			"purpose":         handle.Purpose,
			"secret":          handle.Secret,
		})
		if err != nil {
			return "", err
		}
		return string(body), nil
	})
}

func parseAskUserArgs(args map[string]any) (UserInputRequest, error) {
	prompt, _ := args["prompt"].(string)
	prompt = strings.TrimSpace(prompt)
	if prompt == "" {
		return UserInputRequest{}, &toolrejection.ToolReject{Code: "ASK_USER_PROMPT_REQUIRED"}
	}

	var rt workflowdef.FeedbackResponseType
	if raw, ok := args["response_type"].(string); ok && strings.TrimSpace(raw) != "" {
		rt = workflowdef.FeedbackResponseType(strings.TrimSpace(raw))
	}

	var options []string
	if raw, ok := args["options"].([]any); ok {
		for _, it := range raw {
			if s, ok := it.(string); ok && strings.TrimSpace(s) != "" {
				options = append(options, strings.TrimSpace(s))
			}
		}
	}

	purpose, _ := args["purpose"].(string)
	purpose = strings.TrimSpace(purpose)

	artifacts := parseAskStringIDs(args["artifacts"])
	secret, err := parseSecretInputSpec(args["secret"])
	if err != nil {
		return UserInputRequest{}, err
	}

	return UserInputRequest{
		Prompt:       prompt,
		Purpose:      purpose,
		ResponseType: rt,
		Options:      options,
		Artifacts:    artifacts,
		Secret:       secret,
	}, nil
}

func parseSecretInputSpec(raw any) (*workflowdef.SecretInputSpec, error) {
	if raw == nil {
		return nil, nil
	}
	value, ok := raw.(map[string]any)
	if !ok {
		return nil, &toolrejection.ToolReject{Code: "ASK_USER_SECRET_METADATA_INVALID", Data: map[string]any{"field": "secret"}}
	}
	spec := &workflowdef.SecretInputSpec{
		Name:    strings.TrimSpace(runstate.StringValue(value["name"])),
		Purpose: strings.TrimSpace(runstate.StringValue(value["purpose"])),
		Scope:   strings.TrimSpace(runstate.StringValue(value["scope"])),
	}
	switch n := value["agent_use_ttl_seconds"].(type) {
	case float64:
		if math.Trunc(n) != n || n < 0 || n > float64(maxAskSecretAgentUseLifetimeSeconds) {
			return nil, &toolrejection.ToolReject{Code: "ASK_USER_SECRET_METADATA_INVALID", Data: map[string]any{"field": "secret.agent_use_ttl_seconds"}}
		}
		spec.AgentUseTTLSeconds = int64(n)
	case int:
		spec.AgentUseTTLSeconds = int64(n)
	case int64:
		spec.AgentUseTTLSeconds = n
	case nil:
	default:
		return nil, &toolrejection.ToolReject{Code: "ASK_USER_SECRET_METADATA_INVALID", Data: map[string]any{"field": "secret.agent_use_ttl_seconds"}}
	}
	return spec, nil
}

func parseAskStringIDs(raw any) []string {
	switch v := raw.(type) {
	case []string:
		out := make([]string, 0, len(v))
		for _, s := range v {
			if s = strings.TrimSpace(s); s != "" {
				out = append(out, s)
			}
		}
		return out
	case []any:
		out := make([]string, 0, len(v))
		for _, it := range v {
			if s, ok := it.(string); ok {
				if s = strings.TrimSpace(s); s != "" {
					out = append(out, s)
				}
			}
		}
		return out
	default:
		return nil
	}
}
