package mcp

import (
	"errors"
	"strings"

	"github.com/lycaon/lycaon/internal/httpclient"
)

// MCP settings codes. Merge refusals are Reject* in catalog.go.
const (
	CodeIDRequired                = "mcp_id_required"
	CodeTransportRequired         = "mcp_transport_required"
	CodeTransportConflict         = "mcp_transport_conflict"
	CodeProviderNotFound          = "mcp_provider_not_found"
	CodeUpdateEmpty               = "mcp_update_empty"
	CodeOAuthUnavailable          = "mcp_oauth_unavailable"
	CodeOAuthFailed               = "mcp_oauth_failed"
	CodeOAuthNotSupported         = "mcp_oauth_not_supported"
	CodeOAuthRegistrationRequired = "mcp_oauth_registration_required"
	CodeRecipeNotFound            = "mcp_recipe_not_found"
	CodePersistFailed             = "mcp_persist_failed"
	CodeSyncFailed                = "mcp_sync_failed"
	CodeProviderUnreachable       = "mcp_provider_unreachable"
	// CodeToolPinUnreadable marks a provider held at its last published
	// generation because device tool-pin state could not be read.
	CodeToolPinUnreadable = "mcp_tool_pin_unreadable"
)

// AdminError is a catalog-coded MCP settings failure.
type AdminError struct {
	Code       string
	ProviderID string
	cause      error
}

func (e *AdminError) Error() string {
	if e == nil {
		return ""
	}
	switch {
	case e.cause != nil && e.ProviderID != "":
		return e.Code + ": " + e.ProviderID + ": " + e.cause.Error()
	case e.cause != nil:
		return e.Code + ": " + e.cause.Error()
	case e.ProviderID != "":
		return e.Code + ": " + e.ProviderID
	default:
		return e.Code
	}
}

func (e *AdminError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.cause
}

func AdminErr(code string) *AdminError {
	return &AdminError{Code: code}
}

func AdminErrID(code, id string) *AdminError {
	return &AdminError{Code: code, ProviderID: strings.TrimSpace(id)}
}

func AdminWrap(code string, err error) *AdminError {
	return &AdminError{Code: code, cause: err}
}

func persistErr(err error) error {
	if err == nil {
		return nil
	}
	var ae *AdminError
	if errors.As(err, &ae) {
		return err
	}
	var dup *DuplicateIDError
	if errors.As(err, &dup) {
		return AdminErrID(RejectDuplicateID, dup.ID)
	}
	return AdminWrap(CodePersistFailed, err)
}

func oauthErr(err error) error {
	if err == nil {
		return nil
	}
	var ae *AdminError
	if errors.As(err, &ae) {
		return err
	}
	if errors.Is(err, ErrOAuthRegistrationRequired) {
		return AdminErr(CodeOAuthRegistrationRequired)
	}
	if httpclient.Unreachable(err) {
		return AdminWrap(CodeProviderUnreachable, err)
	}
	return AdminWrap(CodeOAuthFailed, err)
}

func syncErr(err error) error {
	if err == nil {
		return nil
	}
	var ae *AdminError
	if errors.As(err, &ae) {
		return err
	}
	return AdminWrap(SyncFailureCode(err), err)
}

func SyncFailureCode(err error) string {
	if err == nil {
		return ""
	}
	if httpclient.Unreachable(err) {
		return CodeProviderUnreachable
	}
	return CodeSyncFailed
}
