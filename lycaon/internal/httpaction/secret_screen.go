package httpaction

import (
	"context"
	"github.com/lycaon/lycaon/internal/toolrejection"
	"strings"

	"github.com/lycaon/lycaon/internal/outboundhttp"
	"github.com/lycaon/lycaon/internal/secretmatch"
	"github.com/lycaon/lycaon/internal/tools"
)

type outboundRequest struct {
	url       string
	headers   []outboundhttp.Header
	body      []byte
	protected map[int][]string
	// redacted reports that the sent bytes differ everywhere the screen matched.
	redacted bool
	receipt  string
}

// requestField preserves the header label for both screening and redaction.
type requestField struct {
	label     string
	value     string
	protected []string
}

func (r outboundRequest) fields() []requestField {
	fields := make([]requestField, 0, len(r.headers)+2)
	fields = append(fields, requestField{value: r.url})
	for _, header := range r.headers {
		fields = append(fields, requestField{label: header.Name, value: header.Value})
	}
	fields = append(fields, requestField{value: string(r.body)})
	for i := range fields {
		fields[i].protected = r.protected[i]
	}
	return fields
}

// redact rewrites every field under the label its screen used.
func (r outboundRequest) redact(ctx context.Context, matcher *secretmatch.Matcher) outboundRequest {
	fields := r.fields()
	out := outboundRequest{
		url:       redactField(ctx, matcher, fields[0]),
		headers:   make([]outboundhttp.Header, len(r.headers)),
		body:      []byte(redactField(ctx, matcher, fields[len(fields)-1])),
		protected: r.protected,
	}
	for i, header := range r.headers {
		out.headers[i] = outboundhttp.Header{
			Name:  header.Name,
			Value: redactField(ctx, matcher, fields[i+1]),
		}
	}
	return out
}

func screenFields(ctx context.Context, matcher *secretmatch.Matcher, fields []requestField) []secretmatch.Match {
	var matches []secretmatch.Match
	for _, field := range fields {
		matches = append(matches, screenField(ctx, matcher, field)...)
	}
	return matches
}

func screenField(ctx context.Context, matcher *secretmatch.Matcher, field requestField) []secretmatch.Match {
	return secretmatch.OutsideProtected(field.value, matcher.ScreenLabeledContext(ctx, field.label, field.value), field.protected)
}

func redactField(ctx context.Context, matcher *secretmatch.Matcher, field requestField) string {
	return secretmatch.RedactEvidence(field.value, screenField(ctx, matcher, field))
}

// requestDestination identifies the recipient: the reviewed socket when the
// request travels over one, otherwise the URL origin.
func requestDestination(spec requestSpec) (id, label string) {
	if spec.socket != "" {
		label = "unix:" + spec.socket
		return secretmatch.DestinationKey(label, "unix", spec.socket), label
	}
	return secretmatch.HTTPDestination(spec.target)
}

// primaryMatch selects the card's highest-severity finding.
func primaryMatch(matches []secretmatch.Match) secretmatch.Match {
	primary := matches[0]
	for _, match := range matches[1:] {
		if match.Severity == "critical" && primary.Severity != "critical" {
			primary = match
		}
	}
	return primary
}

func screenFaultReject(stage string) *toolrejection.ToolReject {
	return &toolrejection.ToolReject{
		Code: toolrejection.OutboundSecretScreenFailedCode,
		Data: map[string]any{"surface": "http_request", "fault_stage": stage},
	}
}

// screenRequest screens the serialized request; jar placement is governed separately.
func screenRequest(ctx context.Context, deps Deps, spec requestSpec, args map[string]any, tc tools.ToolContext) (outboundRequest, error) {
	classification := deps.SecretMatcher.ClassificationSnapshot(ctx)
	ctx = deps.SecretMatcher.WithClassifications(ctx, classification)
	sending := requestWire(spec, args, tc.Secrets)
	evidence, err := requestArgumentEvidence(ctx, deps.SecretMatcher, args, tc.Secrets)
	if err != nil {
		return outboundRequest{}, screenFaultReject(secretmatch.FaultStageScreenUnwired)
	}
	matches := secretmatch.MergeEvidence(evidence, screenFields(ctx, deps.SecretMatcher, sending.fields()))
	if len(matches) == 0 {
		return sending, nil
	}
	match := primaryMatch(matches)
	destinationID, destinationLabel := requestDestination(spec)
	if deps.SecretAsk == nil {
		return outboundRequest{}, screenFaultReject(secretmatch.FaultStageScreenUnwired)
	}
	path := "request"
	origin := secretmatch.OriginField
	if spec.body.sourcePath != "" {
		path, origin = spec.body.sourcePath, secretmatch.OriginFile
	}
	reviewValue := ""
	for _, field := range sending.fields() {
		for _, hit := range screenField(ctx, deps.SecretMatcher, field) {
			if hit.Fingerprint == match.Fingerprint {
				reviewValue = secretmatch.ReviewValue(field.value, hit)
			}
		}
	}
	resolution, err := deps.SecretAsk(ctx, secretmatch.Alert{
		ReviewValue: reviewValue,
		Surface:     secretmatch.SurfaceHTTPRequest, DestinationID: destinationID, DestinationLabel: destinationLabel,
		SecretNames: secretmatch.ManagedNames(matches),
		RuleID:      match.RuleID, RuleTitle: match.Title, GenericShape: match.GenericShape,
		Occurrences: len(matches), SourceKind: secretmatch.SourceToolArgument, SourceTool: "http_request",
		SourcePath: path, OriginKind: origin, Fingerprints: secretmatch.Fingerprints(matches),
		VarName: match.VarName, Container: match.Container, ContestToken: strings.TrimSpace(spec.contest),
		RecipientsLocal: spec.dialsLoopback(),
	})
	if err != nil {
		stage := secretmatch.FaultStageRaise
		if fault, ok := secretmatch.Faulted(err); ok {
			stage = fault.Stage
		}
		return outboundRequest{}, screenFaultReject(stage)
	}
	if err := deps.SecretMatcher.CheckClassification(ctx, classification); err != nil {
		return outboundRequest{}, screenFaultReject(secretmatch.FaultStageRaise)
	}
	if resolution.Decision.Blocks() {
		tc.Secrets.Withhold(ctx)
		reject := &toolrejection.ToolReject{Code: toolrejection.OutboundSecretDeniedCode, Data: map[string]any{
			"surface": "http_request", "rule_id": match.RuleID, "host": destinationLabel, "shape": match.GenericShape,
		}}
		toolrejection.AttachUserGuidance(reject, resolution.Guidance)
		return outboundRequest{}, reject
	}
	if resolution.Decision != secretmatch.SendRedacted {
		return sending, nil
	}
	redactedArgs := redactRequestArguments(ctx, deps.SecretMatcher, args, tc)
	rebuilt, err := parseRequestSpec(ctx, deps.Boundary, tc, redactedArgs)
	if err != nil {
		return outboundRequest{}, screenFaultReject(secretmatch.FaultStageRedactUnsupported)
	}
	rewritten := requestWire(rebuilt, redactedArgs, tc.Secrets).redact(ctx, deps.SecretMatcher)
	// A receipt may only claim what the rewrite actually removed.
	if len(screenFields(ctx, deps.SecretMatcher, rewritten.fields())) > 0 {
		return outboundRequest{}, screenFaultReject(secretmatch.FaultStageRedactUnsupported)
	}
	tc.Secrets.Redacted(spec.outgoing)
	rewritten.redacted, rewritten.receipt = true, resolution.ReceiptToken
	return rewritten, nil
}
