package hitl

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/lycaon/lycaon/internal/secretmatch"
	"github.com/lycaon/lycaon/pkg/api"
)

const originInCommand = "in the command"

// The destination identifies the launched process; onward sends are unobserved.
const destinationCommandProcess = "this command's own process"

// secretDestinationKind maps the screen surface fact onto the wire enum.
func secretDestinationKind(kind secretmatch.DestinationKind) api.ApprovalSecretDestinationKind {
	switch kind {
	case secretmatch.DestinationModelProvider:
		return api.ApprovalSecretDestinationModelProvider
	case secretmatch.DestinationProcess:
		return api.ApprovalSecretDestinationProcess
	case secretmatch.DestinationFile:
		return api.ApprovalSecretDestinationFile
	default:
		return api.ApprovalSecretDestinationService
	}
}

// compileSecretLocation builds presentation.location, which every secret card
// carries.
func compileSecretLocation(secret *SecretScreen) *api.ApprovalSecretLocation {
	if secret == nil {
		return nil
	}
	origin, kind, path, line := secretOriginFace(secret)
	if origin == "" && strings.TrimSpace(secret.CommandLine) != "" {
		// The argv block above already shows the value's place in the command.
		origin, kind = originInCommand, api.ApprovalSecretOriginField
	}
	destKind := secretDestinationKind(secret.DestinationKind)
	destination := strings.TrimSpace(secret.DestinationLabel)
	if destination == "" {
		destination = strings.TrimSpace(secret.DestinationID)
	}
	if destination == "" && destKind == api.ApprovalSecretDestinationProcess {
		// Process recipients may have no service address.
		destination = destinationCommandProcess
	}
	if origin == "" || destination == "" {
		return nil
	}
	loc := &api.ApprovalSecretLocation{
		SecretNames:     append([]string(nil), secret.SecretNames...),
		Recipients:      WireSecretRecipients(secret.Recipients),
		Origin:          origin,
		Destination:     destination,
		OriginKind:      kind,
		DestinationKind: destKind,
	}
	if kind == api.ApprovalSecretOriginFile {
		loc.Path = path
		loc.Line = line
	}
	if id := strings.TrimSpace(secret.SourceToolCallID); id != "" {
		loc.RevealToolCallID = id
	} else if id := strings.TrimSpace(secret.ToolCallID); id != "" && kind == api.ApprovalSecretOriginField {
		loc.RevealToolCallID = id
	}
	return loc
}

func secretOriginFace(secret *SecretScreen) (origin string, kind api.ApprovalSecretOriginKind, path string, line int) {
	sourcePath := strings.TrimSpace(secret.SourcePath)
	switch secret.OriginKind {
	case secretmatch.OriginFile:
		if sourcePath == "" {
			return "", "", "", 0
		}
		origin = sourcePath
		if secret.SourceLine > 0 {
			origin = sourcePath + ":" + strconv.Itoa(secret.SourceLine)
		}
		return origin, api.ApprovalSecretOriginFile, sourcePath, secret.SourceLine
	case secretmatch.OriginField:
		if sourcePath != "" {
			return "in " + sourcePath, api.ApprovalSecretOriginField, "", 0
		}
		switch secretmatch.SourceKind(strings.TrimSpace(secret.SourceKind)) {
		case secretmatch.SourceUserMessage:
			return "in your message", api.ApprovalSecretOriginField, "", 0
		case secretmatch.SourceAssistantMessage:
			return "in the agent reply", api.ApprovalSecretOriginField, "", 0
		case secretmatch.SourceSystemMessage:
			return "in the system prompt", api.ApprovalSecretOriginField, "", 0
		case secretmatch.SourceToolResult:
			return "in a tool result", api.ApprovalSecretOriginField, "", 0
		case secretmatch.SourceToolCall, secretmatch.SourceToolArgument:
			return "in a tool call", api.ApprovalSecretOriginField, "", 0
		case secretmatch.SourceToolDefinition:
			return "in a tool definition", api.ApprovalSecretOriginField, "", 0
		case secretmatch.SourceVisualCapture:
			return "in an image", api.ApprovalSecretOriginField, "", 0
		}
	}
	return "", "", "", 0
}

func secretLocationLine(loc *api.ApprovalSecretLocation) string {
	if loc == nil {
		return ""
	}
	origin := strings.TrimSpace(loc.Origin)
	destination := strings.TrimSpace(loc.Destination)
	if origin == "" || destination == "" {
		return ""
	}
	return origin + " → " + destination
}

func (p ApprovalPlan) validateSecretLocation() error {
	loc := p.Presentation.Location
	if loc == nil {
		return nil
	}
	if p.Subject.Kind != ApprovalSubjectSecret && !hasSecretReason(p.Reasons) {
		return fmt.Errorf("approval location requires a secret disclosure reason")
	}
	if strings.TrimSpace(loc.Origin) == "" || strings.TrimSpace(loc.Destination) == "" {
		return fmt.Errorf("secret location is incomplete")
	}
	switch loc.OriginKind {
	case api.ApprovalSecretOriginFile:
		if strings.TrimSpace(loc.Path) == "" {
			return fmt.Errorf("file secret location has no path")
		}
	case api.ApprovalSecretOriginField:
		if strings.TrimSpace(loc.Path) != "" || loc.Line != 0 {
			return fmt.Errorf("field secret location carries a file path")
		}
	default:
		return fmt.Errorf("secret location origin kind %q is unknown", loc.OriginKind)
	}
	switch loc.DestinationKind {
	case api.ApprovalSecretDestinationModelProvider, api.ApprovalSecretDestinationService,
		api.ApprovalSecretDestinationProcess, api.ApprovalSecretDestinationFile:
	default:
		return fmt.Errorf("secret location destination kind %q is unknown", loc.DestinationKind)
	}
	return nil
}

func hasSecretReason(reasons []api.ApprovalGate) bool {
	for _, reason := range reasons {
		if reason == api.GateSecretOutbound {
			return true
		}
	}
	return false
}

func WireSecretRecipients(recipients []secretmatch.Recipient) []api.ApprovalSecretRecipient {
	var out []api.ApprovalSecretRecipient
	for _, recipient := range recipients {
		out = append(out, api.ApprovalSecretRecipient{Label: recipient.Label, Surface: string(recipient.Surface), Kind: secretDestinationKind(recipient.Kind)})
	}
	return out
}
