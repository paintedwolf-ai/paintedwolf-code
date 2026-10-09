package httpaction

import (
	"context"
	"github.com/lycaon/lycaon/internal/toolrejection"
	"sort"
	"strings"

	"github.com/lycaon/lycaon/internal/secretmatch"
	"github.com/lycaon/lycaon/internal/tools"
)

// releaseForeignTokens asks the outbound secret screen before a token issued
// by another service is placed in this request. Placement at the issuer is
// the jar's purpose and needs no review.
func releaseForeignTokens(
	ctx context.Context, deps Deps, spec requestSpec, tc tools.ToolContext, jarName string, placements []tokenPlacement,
) (secretmatch.Resolution, *toolrejection.ToolReject) {
	var foreign []tokenPlacement
	for _, placement := range placements {
		if placement.foreign {
			foreign = append(foreign, placement)
		}
	}
	if len(foreign) == 0 {
		return secretmatch.Resolution{Decision: secretmatch.SendUnchanged}, nil
	}
	if deps.SecretAsk == nil {
		return secretmatch.Resolution{}, screenFaultReject(secretmatch.FaultStageScreenUnwired)
	}
	destinationID, destinationLabel := requestDestination(spec)
	names := make([]string, 0, len(foreign))
	issuers := make(map[string]string, len(foreign))
	for _, placement := range foreign {
		names = append(names, placement.name)
		issuers[placement.name] = placement.token.OriginLabel
	}
	sort.Strings(names)
	resolution, err := deps.SecretAsk(ctx, secretmatch.Alert{
		Surface: secretmatch.SurfaceHTTPRequest, DestinationID: destinationID, DestinationLabel: destinationLabel,
		SecretNames: names, RuleID: secretmatch.ManagedRuleID, RuleTitle: secretmatch.ManagedRuleTitle,
		GenericShape: secretmatch.GenericShape(foreign[0].token.Value), Occurrences: len(foreign),
		SourceKind: secretmatch.SourceToolArgument, SourceTool: "http_request", SourcePath: "request",
		OriginKind: secretmatch.OriginField, Fingerprints: tokenFingerprints(deps.SecretMatcher, foreign),
		VarName: foreign[0].name, Container: "token jar " + jarName,
		ContestToken: strings.TrimSpace(spec.contest), RecipientsLocal: spec.dialsLoopback(),
	})
	if err != nil {
		stage := secretmatch.FaultStageRaise
		if fault, ok := secretmatch.Faulted(err); ok {
			stage = fault.Stage
		}
		return secretmatch.Resolution{}, screenFaultReject(stage)
	}
	if resolution.Decision.Blocks() {
		tc.Secrets.Withhold(ctx)
		reject := &toolrejection.ToolReject{Code: toolrejection.OutboundSecretDeniedCode, Data: map[string]any{
			"surface": "http_request", "rule_id": secretmatch.ManagedRuleID, "host": destinationLabel,
			"shape": secretmatch.GenericShape(foreign[0].token.Value), "tokens": names, "issuers": issuers,
		}}
		toolrejection.AttachUserGuidance(reject, resolution.Guidance)
		return secretmatch.Resolution{}, reject
	}
	return resolution, nil
}

// tokenFingerprints identifies the placed values for approval reuse without
// carrying them.
func tokenFingerprints(matcher *secretmatch.Matcher, placements []tokenPlacement) []secretmatch.SecretFingerprint {
	var fingerprints []secretmatch.SecretFingerprint
	for _, placement := range placements {
		match, err := matcher.ManagedValue(placement.token.Value, placement.name, "")
		if err != nil {
			return nil
		}
		fingerprints = append(fingerprints, match.Fingerprint)
	}
	return fingerprints
}
