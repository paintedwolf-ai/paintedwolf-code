package httpaction

import (
	"context"

	"github.com/lycaon/lycaon/internal/outboundhttp"
	"github.com/lycaon/lycaon/internal/secretmatch"
	"github.com/lycaon/lycaon/internal/toolrejection"
	"github.com/lycaon/lycaon/internal/tools"
)

const clientIdentityShape = "Client certificate"

// reviewClientIdentity asks before a request presents a client certificate:
// the handshake proves this identity to the recipient, a credential delivery
// like any other. The review names the certificate and the recipient, so a
// remembered approval covers that pair and nothing else.
func reviewClientIdentity(ctx context.Context, deps Deps, spec requestSpec, tc tools.ToolContext, material *outboundhttp.ClientTLS) *toolrejection.ToolReject {
	if material == nil || len(material.Certificates) == 0 {
		return nil
	}
	if deps.SecretAsk == nil || deps.SecretMatcher == nil {
		return screenFaultReject(secretmatch.FaultStageScreenUnwired)
	}
	var fingerprints []secretmatch.SecretFingerprint
	for _, certificate := range material.Certificates {
		if len(certificate.Certificate) == 0 {
			continue
		}
		match, err := deps.SecretMatcher.ManagedValue(string(certificate.Certificate[0]), "client certificate", "")
		if err != nil {
			return screenFaultReject(secretmatch.FaultStageScreenUnwired)
		}
		fingerprints = append(fingerprints, match.Fingerprint)
	}
	destinationID, destinationLabel := requestDestination(spec)
	resolution, err := deps.SecretAsk(ctx, secretmatch.Alert{
		Surface: secretmatch.SurfaceHTTPRequest, DestinationID: destinationID, DestinationLabel: destinationLabel,
		SecretNames: []string{"client certificate"}, RuleID: secretmatch.ManagedRuleID, RuleTitle: clientIdentityShape,
		GenericShape: clientIdentityShape, Occurrences: len(fingerprints),
		SourceKind: secretmatch.SourceToolArgument, SourceTool: "http_request", SourcePath: "tls",
		OriginKind: secretmatch.OriginField, Fingerprints: fingerprints, Container: "client certificate",
		RecipientsLocal: spec.dialsLoopback(),
	})
	if err != nil {
		stage := secretmatch.FaultStageRaise
		if fault, ok := secretmatch.Faulted(err); ok {
			stage = fault.Stage
		}
		return screenFaultReject(stage)
	}
	if resolution.Decision != secretmatch.SendUnchanged {
		tc.Effects.Secrets.Withhold(ctx)
		reject := &toolrejection.ToolReject{Code: toolrejection.OutboundSecretDeniedCode, Data: map[string]any{
			"surface": "http_request", "rule_id": secretmatch.ManagedRuleID, "host": destinationLabel, "shape": clientIdentityShape,
		}}
		toolrejection.AttachUserGuidance(reject, resolution.Guidance)
		return reject
	}
	return nil
}
