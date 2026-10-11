package toolexecution

import (
	"context"
	"errors"
	"github.com/lycaon/lycaon/internal/toolrejection"
	"github.com/lycaon/lycaon/internal/tools"
	"strings"

	"github.com/lycaon/lycaon/internal/secretcap"
)

// SetSecretResolver installs host-side capability substitution.
func (e *Secrets) SetSecretResolver(resolver *secretcap.Service) {
	if e == nil {
		return
	}
	if resolver == nil {
		e.secretResolver = nil
		return
	}
	e.secretResolver = resolver.Resolve
}

// resolveSecretReferences resolves only for tools whose contract names the
// outbound screen that reviews the substituted copy.
func (e *Secrets) resolveSecretReferences(
	ctx context.Context,
	tool string,
	args map[string]any,
	tc tools.ToolContext,
) (*secretcap.Resolution, error) {
	if !tc.Invocation.Contract.AcceptsSecretReferences() {
		return &secretcap.Resolution{Arguments: args}, nil
	}
	if resolveRef, ok := args["resolve_secret_references"].(bool); ok && !resolveRef {
		return &secretcap.Resolution{Arguments: args}, nil
	}
	use := secretcap.ReferenceUseInSlots(args, tc.Invocation.Contract.SecretReferenceArgs)
	if use.Malformed {
		return nil, errSecretReferenceMalformed
	}
	if !use.Complete {
		return &secretcap.Resolution{Arguments: args}, nil
	}
	if e == nil || e.secretResolver == nil {
		return nil, errSecretReferenceUnavailable
	}
	return e.secretResolver(ctx, args, secretcap.ResolveContext{
		ProjectID: strings.TrimSpace(tc.Identity.ProjectID), ChatSessionID: tc.ChatSessionID(),
		SessionID: strings.TrimSpace(tc.Identity.SessionID), ToolName: strings.TrimSpace(tool), ToolCallID: tc.Identity.ToolCallID,
		Slots: tc.Invocation.Contract.SecretReferenceArgs,
	})
}

var (
	errSecretReferenceUnavailable = errors.New("secret reference resolver is unavailable")
	// A damaged token would otherwise reach the consumer as literal text.
	errSecretReferenceMalformed = errors.New("secret reference is malformed")
)

func secretReferenceReject(err error) *toolrejection.ToolReject {
	code := "SECRET_REFERENCE_FAILED"
	retryable := false
	switch {
	case errors.Is(err, errSecretReferenceUnavailable):
		code = "SECRET_REFERENCE_UNAVAILABLE"
	case errors.Is(err, errSecretReferenceMalformed):
		code, retryable = "SECRET_REFERENCE_MALFORMED", true
	case errors.Is(err, secretcap.ErrNotFound):
		code, retryable = "SECRET_REFERENCE_NOT_FOUND", true
	case errors.Is(err, secretcap.ErrNotVisible):
		code, retryable = "SECRET_REFERENCE_OUT_OF_SCOPE", true
	case errors.Is(err, secretcap.ErrRevoked):
		code, retryable = "SECRET_REFERENCE_REVOKED", true
	case errors.Is(err, secretcap.ErrAgentUseExpired):
		code, retryable = "SECRET_REFERENCE_AGENT_USE_EXPIRED", true
	case errors.Is(err, secretcap.ErrValueMissing):
		code = "SECRET_REFERENCE_VALUE_UNAVAILABLE"
	case errors.Is(err, secretcap.ErrTooManyReferences):
		code, retryable = "SECRET_REFERENCE_LIMIT", true
	}
	reject := &toolrejection.ToolReject{Code: code, Retryable: retryable}
	var limit *secretcap.ReferenceLimitError
	if errors.As(err, &limit) {
		reject.Data = map[string]any{"secret_reference_count": limit.Count, "secret_reference_limit": limit.Limit}
	}
	return reject
}
