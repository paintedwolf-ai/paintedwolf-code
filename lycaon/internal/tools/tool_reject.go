package tools

import (
	"fmt"
	"strings"

	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/hostmarker"
	"github.com/lycaon/lycaon/internal/isolation"
	"github.com/lycaon/lycaon/pkg/api"
)

// Outbound secret screens share one public outcome vocabulary.
const (
	// OutboundSecretDeniedCode means the reviewed send did not proceed.
	OutboundSecretDeniedCode = "OUTBOUND_SECRET_DENIED"
	// OutboundSecretScreenFailedCode means review could not be requested.
	OutboundSecretScreenFailedCode = "OUTBOUND_SECRET_SCREEN_FAILED"
)

// ToolReject carries a structured tool-failure observation.
type ToolReject struct {
	ArgumentValidation bool `json:"-"`
	Code               string
	Observation        string // snake_case observation token (≠ Decision hint id)
	Data               map[string]any
	FailureClass       string
	Retryable          bool
	OwnerRef           string
}

// CompleteFailureMetadata fills invocation failure fields.
func CompleteFailureMetadata(reject *ToolReject, tool, owner string) *ToolReject {
	if reject == nil {
		return nil
	}
	outcome, isolationReject := isolation.Lookup(strings.TrimSpace(reject.Code))
	if isolationReject {
		reject.FailureClass = api.FailureClassIsolationRejection
		reject.Retryable = outcome.Retryable()
	} else if strings.TrimSpace(reject.FailureClass) == "" {
		reject.FailureClass = api.FailureClassHostRejection
	}
	if strings.TrimSpace(reject.OwnerRef) == "" {
		reject.OwnerRef = strings.TrimSpace(owner)
	}
	if reject.Data == nil {
		reject.Data = map[string]any{}
	}
	if _, ok := reject.Data["tool"]; !ok {
		reject.Data["tool"] = strings.TrimSpace(tool)
	}
	reject.Data["failure_class"] = reject.FailureClass
	reject.Data["retryable"] = reject.Retryable
	if isolationReject {
		reject.Data["isolation_disposition"] = string(outcome.Disposition)
	}
	if reject.OwnerRef != "" {
		reject.Data["owner_ref"] = reject.OwnerRef
	}
	return reject
}

func (e *ToolReject) Error() string {
	if e == nil {
		return ""
	}
	if e.Observation != "" {
		return e.Observation
	}
	return e.Code
}

// MachineErrorCode returns the declared reject Code for OAR mcp_error_code.
func (e *ToolReject) MachineErrorCode() string {
	if e == nil {
		return ""
	}
	return strings.TrimSpace(e.Code)
}

func (e *ToolReject) observationToken() string {
	if e == nil {
		return ""
	}
	if obs := strings.TrimSpace(e.Observation); obs != "" {
		return obs
	}
	code := strings.TrimSpace(e.Code)
	if code == "" {
		return ""
	}
	if obs, ok := rejectCodeObservation[code]; ok {
		return obs
	}
	// Unknown codes publish as a snake_case observation token.
	parts := strings.Split(strings.ToLower(code), "_")
	if len(parts) > 1 {
		return strings.Join(parts[1:], "_")
	}
	return parts[0]
}

// ToolOwnerFailedCode is the subsystem-owner error Code when a handler cannot settle.
const ToolOwnerFailedCode = "TOOL_OWNER_FAILED"

// ToolOwnerInterruptedCode identifies an invocation stopped before settlement.
const ToolOwnerInterruptedCode = "TOOL_OWNER_INTERRUPTED"

// VerifyUnverifiableCode is the stated Code on a settled unverifiable receipt.
const VerifyUnverifiableCode = "VERIFY_UNVERIFIABLE"

// EditorConfigMismatchCode is stated on a landed write whose text breaks rules
// the file's .editorconfig chain declares. The write still stands.
const EditorConfigMismatchCode = "EDITORCONFIG_MISMATCH"

// HTTPRequestWebPageCode is stated on an http_request that delivered a remote
// HTML document while fetch_url was on the turn's plan. The exchange stands.
const HTTPRequestWebPageCode = "HTTP_REQUEST_WEB_PAGE"

// CommandNotFoundCode identifies a missing executable for habit recovery.
const CommandNotFoundCode = "COMMAND_NOT_FOUND"

// HostRefusal returns a host-authored refusal unchanged.
func HostRefusal(err error) error {
	if reject, ok := guidance.RefusalFromError(err); ok {
		return reject
	}
	return nil
}

// FormatDecisionReject formats an OAR Decision code with template data.
func FormatDecisionReject(code string, data map[string]any, formatter *guidance.StaticRejectFormatter) error {
	code = strings.TrimSpace(code)
	if code == "" {
		return fmt.Errorf("tool rejected")
	}
	if data == nil {
		data = map[string]any{}
	}
	// Preserve structured failure facts through rendering.
	return renderRejectBlock(code, data, &ToolReject{Code: code, Data: data}, formatter)
}

// RenderReject formats a ToolReject and preserves it as the cause.
func RenderReject(rej *ToolReject, formatter *guidance.StaticRejectFormatter) error {
	if rej == nil {
		return nil
	}
	code := strings.TrimSpace(rej.Code)
	if code == "" {
		return rej
	}
	return renderRejectBlock(code, rej.Data, rej, formatter)
}

// renderRejectBlock falls back to the code and subsystem owner's reason.
func renderRejectBlock(
	code string,
	data map[string]any,
	cause *ToolReject,
	formatter *guidance.StaticRejectFormatter,
) error {
	if formatter != nil {
		if formatted, err := formatter.Format(code, data); err == nil {
			return guidance.NewRefusal(code, formatted).WithDetails(data, nil).WithCause(cause)
		}
	}
	block := fmt.Sprintf("%s %s\n%s %s", hostmarker.Rejected, code, hostmarker.CodeLine, code)
	if reason, ok := data["reason"].(string); ok {
		if reason = strings.TrimSpace(reason); reason != "" {
			block += "\nCause: " + reason
		}
	}
	return guidance.NewRefusal(code, block).WithDetails(data, nil).WithCause(cause)
}

// SourceAnalysisUnavailableCode marks a partial result with unavailable source analysis.
const SourceAnalysisUnavailableCode = "SOURCE_ANALYSIS_UNAVAILABLE"
