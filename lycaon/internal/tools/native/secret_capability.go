package native

import (
	"context"
	"errors"
	"fmt"
	"github.com/lycaon/lycaon/internal/toolrejection"
	"strings"
	"time"

	"github.com/lycaon/lycaon/internal/secretcap"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/tools/surveyjson"
)

const (
	SecretGenerateTool = "secret_generate"
	SecretListTool     = "secret_list"
	SecretRevokeTool   = "secret_revoke"

	// SecretRevokeHumanOwnedCode refuses agent revocation of a person's own material.
	SecretRevokeHumanOwnedCode = "SECRET_REVOKE_HUMAN_OWNED"
)

func generateSecret(ctx context.Context, service *secretcap.Service, args map[string]any, tctx tools.ToolContext) (string, error) {
	name, err := secretGenerateString(args, "name", false)
	if err != nil {
		return "", err
	}
	purpose, err := secretGenerateString(args, "purpose", true)
	if err != nil {
		return "", err
	}
	scope, err := secretGenerateString(args, "scope", true)
	if err != nil {
		return "", err
	}
	format, err := secretGenerateString(args, "format", true)
	if err != nil {
		return "", err
	}
	byteCount, err := secretGenerateInteger(args, "bytes", 16, 128, 32)
	if err != nil {
		return "", err
	}
	agentUseTTLSeconds, err := secretGenerateInteger(args, "agent_use_ttl_seconds", 60, 31536000, 0)
	if err != nil {
		return "", err
	}
	meta, err := service.Generate(ctx, secretcap.GenerateRequest{
		ProjectID: tctx.Identity.ProjectID, ChatSessionID: tctx.ChatSessionID(), SessionID: tctx.Identity.SessionID,
		OperationID: tctx.Identity.ToolCallID, Name: name, Purpose: purpose, Scope: scope, Format: format,
		Bytes: byteCount, AgentUseTTL: time.Duration(agentUseTTLSeconds) * time.Second,
	})
	if err != nil {
		return "", secretCapabilityFailure(err)
	}
	encoded, err := surveyjson.Marshal(map[string]any{
		"secret": meta, "created": true, "value_disclosed": false,
		"usage": "Use the reference token in a supported outbound tool field; the host substitutes it only after policy and approval checks.",
	})
	return string(encoded), err
}

func secretCapabilityFailure(err error) error {
	code, retryable := "SECRET_STORE_UNAVAILABLE", false
	switch {
	case errors.Is(err, secretcap.ErrInvalidReference):
		code, retryable = "SECRET_REFERENCE_INVALID", true
	case errors.Is(err, secretcap.ErrNotFound):
		code, retryable = "SECRET_REFERENCE_NOT_FOUND", true
	case errors.Is(err, secretcap.ErrNotVisible):
		code, retryable = "SECRET_REFERENCE_OUT_OF_SCOPE", true
	case errors.Is(err, secretcap.ErrValueMissing):
		code = "SECRET_REFERENCE_VALUE_UNAVAILABLE"
	case errors.Is(err, secretcap.ErrHumanAuthored):
		code = SecretRevokeHumanOwnedCode
	case errors.Is(err, secretcap.ErrInvalidGenerate):
		code, retryable = "SECRET_GENERATE_INVALID", true
	}
	reject := &toolrejection.ToolReject{Code: code, Retryable: retryable}
	var invalid *secretcap.ValidationError
	if code == "SECRET_GENERATE_INVALID" && errors.As(err, &invalid) {
		reject.Data = map[string]any{"field": invalid.Field, "reason": invalid.Reason}
	}
	return reject
}

func secretGenerateInteger(args map[string]any, field string, minimum, maximum, fallback int) (int, error) {
	raw, present := args[field]
	if !present {
		return fallback, nil
	}
	var value int
	switch n := raw.(type) {
	case int:
		value = n
	case int64:
		if n < int64(minimum) || n > int64(maximum) {
			return 0, invalidSecretGenerateInteger(field, minimum, maximum)
		}
		value = int(n)
	case float64:
		if n < float64(minimum) || n > float64(maximum) || float64(int(n)) != n {
			return 0, invalidSecretGenerateInteger(field, minimum, maximum)
		}
		value = int(n)
	default:
		return 0, invalidSecretGenerateInteger(field, minimum, maximum)
	}
	if value < minimum || value > maximum {
		return 0, invalidSecretGenerateInteger(field, minimum, maximum)
	}
	return value, nil
}

func invalidSecretGenerateInteger(field string, minimum, maximum int) error {
	return &toolrejection.ToolReject{Code: "SECRET_GENERATE_INVALID", Retryable: true, Data: map[string]any{"field": field, "reason": "must be an integer within the declared bounds", "min": minimum, "max": maximum}}
}

func secretGenerateString(args map[string]any, field string, optional bool) (string, error) {
	raw, present := args[field]
	if !present && optional {
		return "", nil
	}
	value, ok := raw.(string)
	if !ok || strings.TrimSpace(value) == "" {
		return "", &toolrejection.ToolReject{Code: "SECRET_GENERATE_INVALID", Retryable: true, Data: map[string]any{"field": field, "reason": "must be a nonempty string when supplied"}}
	}
	return value, nil
}

func GenerateHandler(service *secretcap.Service) tools.ToolHandler {
	return func(ctx context.Context, args map[string]any, tctx tools.ToolContext) (string, error) {
		return generateSecret(ctx, service, args, tctx)
	}
}

func ListHandler(service *secretcap.Service) tools.ToolHandler {
	return func(ctx context.Context, _ map[string]any, tctx tools.ToolContext) (string, error) {
		items, err := service.List(ctx, tctx.Identity.ProjectID, tctx.ChatSessionID())
		if err != nil {
			return "", secretCapabilityFailure(err)
		}
		encoded, err := surveyjson.Marshal(map[string]any{"items": items, "count": len(items), "values_disclosed": false})
		return string(encoded), err
	}
}

func RevokeHandler(service *secretcap.Service) tools.ToolHandler {
	return func(ctx context.Context, args map[string]any, tctx tools.ToolContext) (string, error) {
		reference, _ := args["reference"].(string)
		meta, err := service.RevokeByAgent(ctx, tctx.Identity.ProjectID, tctx.ChatSessionID(), reference)
		if err != nil {
			return "", secretCapabilityFailure(err)
		}
		encoded, err := surveyjson.Marshal(map[string]any{"secret": meta, "revoked": true, "value_disclosed": false})
		return string(encoded), err
	}
}

func RegisterSecretCapabilityTools(reg *tools.DefaultRegistry, service *secretcap.Service) error {
	if reg == nil || service == nil {
		return fmt.Errorf("registry and secret capability service required")
	}
	if err := reg.Register(SecretGenerateTool, GenerateHandler(service)); err != nil {
		return err
	}
	if err := reg.Register(SecretListTool, ListHandler(service)); err != nil {
		return err
	}
	return reg.Register(SecretRevokeTool, RevokeHandler(service))
}
