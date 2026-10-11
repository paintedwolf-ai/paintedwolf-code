package toolsecrets

import (
	"strings"

	"github.com/lycaon/lycaon/internal/hitl"
	"github.com/lycaon/lycaon/internal/secretmatch"
)

// SecretReviewPayload is the value-free location and evidence shown on a card.
func SecretReviewPayload(finding secretmatch.Alert, recipients []secretmatch.Recipient, standingRedaction bool) *hitl.SecretScreen {
	surface := strings.TrimSpace(string(finding.Surface))
	if surface == "" {
		surface = "unknown"
	}
	surfaceLabel := strings.TrimSpace(finding.Surface.Label())
	if surfaceLabel == "" {
		surfaceLabel = surface
	}
	destination := strings.TrimSpace(finding.DestinationLabel)
	if destination == "" {
		destination = strings.TrimSpace(finding.DestinationID)
	}
	ruleTitle := strings.TrimSpace(finding.RuleTitle)
	if ruleTitle == "" {
		ruleTitle = strings.TrimSpace(finding.RuleID)
	}
	payload := &hitl.SecretScreen{
		Surface:               surface,
		SurfaceLabel:          surfaceLabel,
		CanRedact:             finding.CanRedact(),
		CanTrack:              finding.Surface == secretmatch.SurfaceModel,
		RedactionNote:         finding.RedactionNote(),
		StandingRedactionHeld: standingRedaction,
		DestinationID:         strings.TrimSpace(finding.DestinationID),
		DestinationLabel:      destination,
		Recipients:            recipients,
		ConnectPorts:          append([]uint16(nil), finding.ConnectPorts...),
		SecretNames:           finding.SecretNames,
		DestinationKind:       finding.Surface.DestinationKind(),
		Managed:               finding.Managed(),
		RedactionBreaks:       finding.Surface.RedactionBreaksRequest(),
		ProviderID:            strings.TrimSpace(finding.ProviderID),
		RuleID:                strings.TrimSpace(finding.RuleID),
		RuleTitle:             ruleTitle,
		GenericShape:          strings.TrimSpace(finding.GenericShape),
		Occurrences:           finding.Occurrences,
		SourceKind:            string(finding.SourceKind),
		SourceTool:            strings.TrimSpace(finding.SourceTool),
		SourcePath:            strings.TrimSpace(finding.SourcePath),
		SourceLine:            finding.SourceLine,
		OriginKind:            finding.OriginKind,
		SourceToolCallID:      strings.TrimSpace(finding.SourceToolCallID),
		ToolCallID:            strings.TrimSpace(finding.ToolCallID),
		CommandLine:           strings.TrimSpace(finding.CommandLine),
		VarName:               strings.TrimSpace(finding.VarName),
		Container:             strings.TrimSpace(finding.Container),
		ScreeningGap:          finding.ScreeningGap,
	}
	return payload
}
