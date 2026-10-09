package inputs

import (
	workflowdef "github.com/lycaon/lycaon/internal/workflow/definition"
	"github.com/lycaon/lycaon/internal/workflow/runstate"
	"strings"
	"unicode/utf8"
)

func askUserInputDigest(norm normalizedAskUser) (string, error) {
	return runstate.CommandDigest("ask_user", struct {
		Prompt    string                           `json:"prompt"`
		Purpose   string                           `json:"purpose"`
		Type      workflowdef.FeedbackResponseType `json:"response_type"`
		Options   []string                         `json:"options"`
		Artifacts []string                         `json:"artifacts"`
		Secret    *workflowdef.SecretInputSpec     `json:"secret,omitempty"`
	}{
		Prompt: norm.prompt, Purpose: norm.purpose, Type: norm.rt,
		Options: append([]string(nil), norm.options...), Artifacts: append([]string(nil), norm.artifactRefs...), Secret: norm.secret,
	})
}
func replayUserInputFromVars(
	vars map[string]any,
	runID, toolCallID, inputDigest string,
) (UserInputHandle, bool, error) {
	toolCallID = strings.TrimSpace(toolCallID)
	if toolCallID == "" {
		return UserInputHandle{}, false, nil
	}
	ask, ok := runstate.CoordinatorAskFromVars(vars)
	if !ok || ask.ToolCallID != toolCallID {
		return UserInputHandle{}, false, nil
	}
	if ask.InputDigest == "" || ask.InputDigest != inputDigest {
		return UserInputHandle{}, false, askUserReject("ASK_USER_OPERATION_CONFLICT", map[string]any{"tool_call_id": toolCallID})
	}
	return UserInputHandle{PhaseID: ask.ID, Prompt: ask.Prompt, ResponseType: ask.ResponseType, Purpose: ask.Purpose, RunID: runID, IssuedRevision: ask.IssuedRevision, Secret: ask.Secret}, true, nil
}
func pendingCoordinatorAskFromVars(vars map[string]any) (string, *workflowdef.UserFeedbackPrompt, bool) {
	if ask, ok := runstate.CoordinatorAskPendingFromVars(vars); ok {
		return ask.ID, &workflowdef.UserFeedbackPrompt{
			Prompt: ask.Prompt, ResponseType: ask.ResponseType, Options: append([]string(nil), ask.Options...),
			AllowOther: ask.AllowOther, ArtifactID: ask.ArtifactID, ArtifactIDs: append([]string(nil), ask.ArtifactIDs...), Purpose: ask.Purpose,
			Secret: ask.Secret,
		}, true
	}
	return "", nil, false
}
func normalizeAskUserRequest(req UserInputRequest) (normalizedAskUser, error) {
	prompt := strings.TrimSpace(req.Prompt)
	if prompt == "" {
		return normalizedAskUser{}, askUserReject("ASK_USER_PROMPT_REQUIRED", nil)
	}
	if n := utf8.RuneCountInString(prompt); n > MaxAskUserPromptRunes {
		return normalizedAskUser{}, askUserReject("ASK_USER_PROMPT_TOO_LONG", map[string]any{
			"max_runes":        MaxAskUserPromptRunes,
			"ask_prompt_runes": n, "ask_prompt_limit": MaxAskUserPromptRunes,
			"runes": n,
		})
	}

	refs := runstate.CleanAskOptions(req.Artifacts)
	n := len(refs)
	if n > askUserCompareMax {
		return normalizedAskUser{}, askUserReject("ASK_USER_COMPARE_ARITY", map[string]any{"count": n, "ask_artifact_count": n, "ask_artifact_limit": askUserCompareMax})
	}

	rawRT := req.ResponseType
	switch rawRT {
	case "", workflowdef.FeedbackResponseText, workflowdef.FeedbackResponseSingleChoice, workflowdef.FeedbackResponseMultiChoice, workflowdef.FeedbackResponseSecret:
	default:
		return normalizedAskUser{}, askUserReject("ASK_USER_RESPONSE_TYPE_INVALID", map[string]any{"response_type": string(rawRT)})
	}
	if rawRT == workflowdef.FeedbackResponseSecret && n > 0 {
		return normalizedAskUser{}, askUserReject("ASK_USER_SECRET_ARTIFACTS_FORBIDDEN", map[string]any{"count": n})
	}
	if rawRT != workflowdef.FeedbackResponseSecret && req.Secret != nil {
		return normalizedAskUser{}, askUserReject("ASK_USER_SECRET_METADATA_FORBIDDEN", map[string]any{"response_type": string(rawRT)})
	}
	reqPurpose := strings.ToLower(strings.TrimSpace(req.Purpose))
	if err := validateAskPurpose(req.Purpose, reqPurpose, n); err != nil {
		return normalizedAskUser{}, err
	}

	options := runstate.CleanAskOptions(req.Options)
	purpose := "clarify"
	var rt workflowdef.FeedbackResponseType

	switch n {
	case 0:
		rt = rawRT
		if rt == "" {
			rt = workflowdef.FeedbackResponseText
		}
		if reqPurpose == "compare" {
			return normalizedAskUser{}, askUserReject("ASK_USER_COMPARE_ARITY", map[string]any{"count": 0, "ask_artifact_count": 0, "ask_artifact_limit": askUserCompareMax})
		}
		if rt == workflowdef.FeedbackResponseSecret {
			if len(options) > 0 {
				return normalizedAskUser{}, askUserReject("ASK_USER_OPTIONS_FORBIDDEN", map[string]any{"response_type": string(rt)})
			}
			if req.Secret == nil || strings.TrimSpace(req.Secret.Name) == "" {
				return normalizedAskUser{}, askUserReject("ASK_USER_SECRET_METADATA_REQUIRED", map[string]any{"field": "secret.name"})
			}
			secret := *req.Secret
			secret.Name = strings.TrimSpace(secret.Name)
			secret.Purpose = strings.TrimSpace(secret.Purpose)
			secret.Scope = strings.TrimSpace(secret.Scope)
			if secret.Scope == "" {
				secret.Scope = "chat"
			}
			if field := invalidSecretMetadataField(secret); field != "" {
				return normalizedAskUser{}, askUserReject("ASK_USER_SECRET_METADATA_INVALID", map[string]any{"field": field})
			}
			if secret.Scope == "project" && secret.Purpose == "" {
				return normalizedAskUser{}, askUserReject("ASK_USER_SECRET_METADATA_REQUIRED", map[string]any{
					"field": "secret.purpose",
				})
			}
			req.Secret = &secret
			purpose = "secret"
		} else if rt.IsChoice() {
			if len(options) < 2 {
				return normalizedAskUser{}, askUserReject("ASK_USER_OPTIONS_REQUIRED", map[string]any{"ask_option_count": len(options), "ask_option_limit": 2})
			}
			if len(options) > MaxAskUserOptions {
				return normalizedAskUser{}, askUserReject("ASK_USER_OPTIONS_TOO_MANY", map[string]any{
					"max_options":      MaxAskUserOptions,
					"ask_option_count": len(options), "ask_option_limit": MaxAskUserOptions,
					"count": len(options),
				})
			}
		} else if len(options) > 0 {
			return normalizedAskUser{}, askUserReject("ASK_USER_OPTIONS_FORBIDDEN", map[string]any{"response_type": string(rt)})
		}
	default:
		// Artifacts default to review; compare is an explicit purpose.
		if reqPurpose == "compare" {
			if n < askUserCompareMin {
				return normalizedAskUser{}, askUserReject("ASK_USER_COMPARE_ARITY", map[string]any{"count": n, "ask_artifact_count": n, "ask_artifact_limit": askUserCompareMax})
			}
			purpose = "compare"
			if len(options) > 0 {
				return normalizedAskUser{}, askUserReject("ASK_USER_COMPARE_OPTIONS", map[string]any{
					"detail": "host synthesizes compare options",
					"count":  n,
				})
			}
			if rawRT != "" && rawRT != workflowdef.FeedbackResponseSingleChoice {
				return normalizedAskUser{}, askUserReject("ASK_USER_COMPARE_OPTIONS", map[string]any{
					"detail": "compare requires single_choice",
					"count":  n,
				})
			}
			rt = workflowdef.FeedbackResponseSingleChoice
			options = synthesizeCompareOptions(n)
		} else {
			purpose = "review"
			if rawRT == workflowdef.FeedbackResponseMultiChoice {
				return normalizedAskUser{}, askUserReject("ASK_USER_REVIEW_OPTIONS", map[string]any{
					"detail": "review does not support multi_choice",
					"count":  n,
				})
			}
			if rawRT == workflowdef.FeedbackResponseText {
				if len(options) > 0 {
					return normalizedAskUser{}, askUserReject("ASK_USER_OPTIONS_FORBIDDEN", map[string]any{"response_type": string(rawRT)})
				}
				rt = workflowdef.FeedbackResponseText
				options = nil
			} else {
				rt = workflowdef.FeedbackResponseSingleChoice
				if len(options) > 0 {
					return normalizedAskUser{}, askUserReject("ASK_USER_REVIEW_OPTIONS", map[string]any{
						"detail": "host synthesizes review options",
						"count":  n,
					})
				}
				options = append([]string(nil), askUserReviewOptions...)
			}
		}
	}

	return normalizedAskUser{
		prompt:       prompt,
		purpose:      purpose,
		rt:           rt,
		options:      options,
		artifactRefs: refs,
		secret:       req.Secret,
	}, nil
}
func validateAskPurpose(raw, purpose string, artifacts int) error {
	fits := true
	switch purpose {
	case "":
	case "clarify":
		fits = artifacts == 0
	case "review":
		fits = artifacts > 0
	case "compare":
		// Arity is reported by ASK_USER_COMPARE_ARITY.
	default:
		fits = false
	}
	if fits {
		return nil
	}
	return askUserReject("ASK_USER_PURPOSE_INVALID", map[string]any{
		"purpose":            raw,
		"ask_purpose":        strings.TrimSpace(raw),
		"ask_purposes":       append([]string(nil), askUserPurposes...),
		"ask_artifact_count": artifacts,
	})
}
func pendingAskRejectData(vars map[string]any) map[string]any {
	if ask, ok := runstate.CoordinatorAskPendingFromVars(vars); ok {
		return map[string]any{"phase_id": ask.ID, "pending_input_id": ask.ID, "prompt": ask.Prompt}
	}
	phaseID, ok := runstate.PendingFeedbackPhase(vars)
	if !ok {
		phaseID = firstPendingDecisionPhase(vars)
	}
	phaseID = strings.TrimSpace(phaseID)
	if phaseID == "" {
		return nil
	}
	data := map[string]any{"phase_id": phaseID, "pending_input_id": phaseID}
	return data
}
func firstPendingDecisionPhase(vars map[string]any) string {
	bucket, _ := vars["user_decision"].(map[string]any)
	if bucket == nil {
		return ""
	}
	for phaseID, raw := range bucket {
		entry, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		if pending, _ := entry["pending"].(bool); pending {
			return strings.TrimSpace(phaseID)
		}
	}
	return ""
}
func synthesizeCompareOptions(n int) []string {
	if n < askUserCompareMin {
		n = askUserCompareMin
	}
	if n > askUserCompareMax {
		n = askUserCompareMax
	}
	out := make([]string, 0, n)
	for i := 0; i < n; i++ {
		out = append(out, askUserCompareLabels[i])
	}
	return out
}
func invalidSecretMetadataField(secret workflowdef.SecretInputSpec) string {
	switch {
	case utf8.RuneCountInString(secret.Name) > 80:
		return "secret.name"
	case utf8.RuneCountInString(secret.Purpose) > 240:
		return "secret.purpose"
	case secret.Scope != "chat" && secret.Scope != "project":
		return "secret.scope"
	case secret.AgentUseTTLSeconds < 0 || secret.AgentUseTTLSeconds > maxAskSecretAgentUseLifetimeSeconds:
		return "secret.agent_use_ttl_seconds"
	default:
		return ""
	}
}

type normalizedAskUser struct {
	prompt       string
	purpose      string
	rt           workflowdef.FeedbackResponseType
	options      []string
	artifactID   string
	artifactIDs  []string
	artifactRefs []string
	secret       *workflowdef.SecretInputSpec
}

func (n *normalizedAskUser) attach(ids []string) {
	switch {
	case len(ids) == 1:
		n.artifactID, n.artifactIDs = ids[0], nil
	case len(ids) >= askUserCompareMin:
		n.artifactID, n.artifactIDs = "", append([]string(nil), ids...)
	}
}
