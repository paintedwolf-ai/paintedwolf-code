package mcp

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/sony/gobreaker"

	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/runeclamp"
	"github.com/lycaon/lycaon/internal/secretcap"
	"github.com/lycaon/lycaon/internal/secretmatch"
	"github.com/lycaon/lycaon/internal/textguard"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/tools/safecmd"
)

// MCPTransportUnavailableCode marks a host-defined transport refusal.
const MCPTransportUnavailableCode = "MCP_TRANSPORT_UNAVAILABLE"

// MCPConsentStateUnavailableCode marks unavailable definition consent state.
const MCPConsentStateUnavailableCode = "MCP_CONSENT_STATE_UNAVAILABLE"

// resolveRunnable selects the enabled project-scoped connection for invocation.
func (r *RegistryImpl) resolveRunnable(ctx context.Context, scope CallScope, providerID string) (MCPProviderEntry, error) {
	_, ok := r.deviceEntry(providerID)
	if !ok {
		return MCPProviderEntry{}, fmt.Errorf("unknown mcp provider: %s", providerID)
	}
	view := r.projectView(ctx, strings.TrimSpace(scope.ProjectDir))
	merged, ok := view.entry(providerID)
	if !ok {
		return MCPProviderEntry{}, fmt.Errorf("unknown mcp provider: %s", providerID)
	}
	if !merged.Enabled {
		if view.applies {
			return MCPProviderEntry{}, fmt.Errorf("mcp provider %q is disabled for this project", providerID)
		}
		return MCPProviderEntry{}, fmt.Errorf("mcp provider %q is disabled", providerID)
	}
	return merged.MCPProviderEntry, nil
}

// resolveForInspection selects the visible project catalog entry.
func (r *RegistryImpl) resolveForInspection(ctx context.Context, scope CallScope, providerID string) (MCPProviderEntry, error) {
	view := r.projectView(ctx, scope.ProjectDir)
	merged, ok := view.entry(providerID)
	if !ok {
		return MCPProviderEntry{}, fmt.Errorf("unknown mcp provider: %s", providerID)
	}
	if !merged.Enabled {
		return MCPProviderEntry{}, fmt.Errorf("mcp provider %q is disabled", providerID)
	}
	return merged.MCPProviderEntry, nil
}

// ListTools returns the qualified tool metadata a provider exposes, for inspection.
func (r *RegistryImpl) ListTools(ctx context.Context, scope CallScope, providerID string) ([]tools.ToolMeta, error) {
	entry, err := r.resolveForInspection(ctx, scope, providerID)
	if err != nil {
		return nil, err
	}
	sess, err := r.ensureSession(ctx, scope, entry)
	if err != nil {
		return nil, err
	}
	toolsList, err := sess.ListTools(ctx)
	if err != nil {
		r.evictDeadSession(scope, providerID, err)
		return nil, err
	}
	out := make([]tools.ToolMeta, 0, len(toolsList))
	for _, tool := range toolsList {
		def := sanitizeToolDefinition(tool)
		out = append(out, tools.ToolMeta{
			Name:              QualifiedToolName(providerID, def.Name),
			Description:       def.Description,
			ArgsSchema:        def.Schema,
			UntrustedMetadata: true,
			ReadOnlyHint:      def.ReadOnly,
			Source:            tools.ToolSourceMCP,
			SourceID:          providerID,
		})
	}
	return out, nil
}

// CallTool invokes one MCP tool on behalf of scope.
func (r *RegistryImpl) CallTool(ctx context.Context, scope CallScope, providerID, toolName string, args map[string]any) (string, error) {
	entry, err := r.resolveRunnable(ctx, scope, providerID)
	if err != nil {
		return "", err
	}
	// Consume the host-only redaction receipt before calling the provider.
	contest, _ := args["unredact"].(string)
	if contest != "" {
		trimmed := make(map[string]any, len(args))
		for key, value := range args {
			if key == "unredact" {
				continue
			}
			trimmed[key] = value
		}
		args = trimmed
	}
	screenedArgs, receipt, err := r.screenCallToolArgs(ctx, entry, toolName, args, contest)
	if err != nil {
		return "", err
	}
	args = screenedArgs
	// The RPC deadline leaves the shared process running.
	ctx, cancel := safecmd.MCPCaps().WithTimeout(ctx)
	defer cancel()
	// Pin the approved definition immediately before invocation.
	if err := r.acceptToolDefinition(providerID, toolName); err != nil {
		return "", &tools.ToolReject{
			Code: MCPConsentStateUnavailableCode,
			Data: map[string]any{"provider": providerID, "tool": toolName},
		}
	}

	br := r.breakerFor(providerID)
	raw, err := br.Execute(func() (any, error) {
		sess, err := r.ensureSession(ctx, scope, entry)
		if err != nil {
			return "", err
		}
		secretcap.ResolutionFrom(ctx).HandOff(ctx, mcpOutboundArgument)
		res, err := sess.CallTool(ctx, toolName, args)
		if err != nil {
			// Evict failed transports before the next call.
			r.evictDeadSession(scope, providerID, err)
			return "", err
		}
		if res.IsError {
			text := ExtractToolResultText(res)
			return "", &tools.ToolReject{
				Code: declaredMCPRejectCode(res.StructuredContent),
				Data: map[string]any{
					"provider":    providerID,
					"tool":        toolName,
					"detail":      runeclamp.ClampBytesMiddle(text, 4096),
					"peer_detail": text,
				},
			}
		}
		return ExtractToolResultText(res), nil
	})
	if err != nil {
		return "", breakerReject(providerID, toolName, err)
	}
	text, _ := raw.(string)
	out, err := shapeMCPOutput(providerID, toolName, text)
	if err == nil {
		// Invisible codepoints are stripped; trust marking comes from message provenance.
		out = textguard.StripInvisibleFormatRunesUntilStable(out)
	}
	if err == nil && receipt.token != "" {
		out += guidance.FormatSecretReceiptMarker(receipt.token, receipt.count)
	}
	return out, err
}

// breakerReject converts breaker refusal into a structured reject.
func breakerReject(providerID, toolName string, err error) error {
	if err == nil {
		return nil
	}
	if !errors.Is(err, gobreaker.ErrOpenState) && !errors.Is(err, gobreaker.ErrTooManyRequests) {
		return err
	}
	return &tools.ToolReject{
		Code: MCPTransportUnavailableCode,
		Data: map[string]any{
			"provider":            providerID,
			"tool":                toolName,
			"detail":              err.Error(),
			"mcp_probe_in_flight": errors.Is(err, gobreaker.ErrTooManyRequests),
		},
	}
}

// mcpRedactionReceipt names a redacted MCP send for a later contest.
type mcpRedactionReceipt struct {
	token string
	count int
}

func (r *RegistryImpl) screenCallToolArgs(ctx context.Context, entry MCPProviderEntry, toolName string, args map[string]any, contestToken string) (map[string]any, mcpRedactionReceipt, error) {
	r.mu.RLock()
	matcher := r.secretMatcher
	ask := r.secretAsk
	r.mu.RUnlock()
	known, knownErr := secretcap.ResolutionFrom(ctx).Matches(matcher, mcpOutboundArgument)
	if knownErr != nil {
		return nil, mcpRedactionReceipt{}, mcpSecretFaultReject("", mcpArgHit{}, secretmatch.NewAskFault(secretmatch.FaultStageScreenUnwired, knownErr))
	}
	classification := matcher.ClassificationSnapshot(ctx)
	ctx = matcher.WithClassifications(ctx, classification)
	hits := mergeMCPSecretEvidence(known, scanMCPArgs(ctx, matcher, args))
	if len(hits) == 0 {
		return args, mcpRedactionReceipt{}, nil
	}
	primary := primaryMCPArgHit(hits)
	transport, _ := entry.Transport()
	destinationLabel := matcher.RedactString(ctx, strings.TrimSpace(entry.ID))
	// Ambient auth is part of the transport: the same URL with a different
	// credential wire, header, token, or environment is a different destination.
	auth := secretmatch.AmbientAuth{
		Wire: entry.CredentialWire, Header: entry.CredentialHeader,
		Token: entry.Token, Headers: entry.Headers, Env: entry.Env,
	}
	destination := secretmatch.DestinationKey(
		destinationLabel,
		string(transport), entry.URL, entry.Command, strings.Join(entry.Args, "\x00"), entry.Version,
		auth.Identity(matcher),
	)
	sourceTool := matcher.RedactString(ctx, strings.TrimSpace(toolName))
	if ask == nil {
		return args, mcpRedactionReceipt{}, mcpSecretFaultReject(
			destination, primary, secretmatch.NewAskFault(secretmatch.FaultStageScreenUnwired, nil))
	}
	resolution, askErr := ask(ctx, secretmatch.Alert{
		ReviewValue:      primary.value,
		Surface:          secretmatch.SurfaceMCP,
		DestinationID:    destination,
		DestinationLabel: destinationLabel,
		RuleID:           primary.match.RuleID,
		RuleTitle:        primary.match.Title,
		GenericShape:     primary.match.GenericShape,
		VarName:          primary.match.VarName,
		Container:        primary.match.Container,
		Occurrences:      len(hits),
		SourceKind:       secretmatch.SourceToolArgument,
		SourceTool:       sourceTool,
		SourcePath:       primary.path,
		OriginKind:       secretmatch.OriginField,
		ToolCallID:       secretmatch.AskAttributionFrom(ctx).ToolCallID,
		Fingerprints:     mcpSecretFingerprints(hits),
		ContestToken:     contestToken,
	})
	if askErr != nil {
		return nil, mcpRedactionReceipt{}, mcpSecretFaultReject(destination, primary, askErr)
	}
	if err := matcher.CheckClassification(ctx, classification); err != nil {
		return nil, mcpRedactionReceipt{}, mcpSecretFaultReject(destination, primary, err)
	}
	if !resolution.Decision.Blocks() {
		if resolution.Decision == secretmatch.SendRedacted {
			secretcap.ResolutionFrom(ctx).Redacted(mcpOutboundArgument)
			redacted := secretcap.ResolutionFrom(ctx).RedactKnown(args).(map[string]any)
			return redactMCPArgs(ctx, matcher, redacted), mcpRedactionReceipt{token: resolution.ReceiptToken, count: len(hits)}, nil
		}
		return args, mcpRedactionReceipt{}, nil
	}
	secretcap.ResolutionFrom(ctx).Withhold(ctx)
	// Blocking outcomes return redaction-safe reject data.
	reject := &tools.ToolReject{
		Code: tools.OutboundSecretDeniedCode,
		Data: map[string]any{
			"surface": string(secretmatch.SurfaceMCP),
			"rule_id": strings.TrimSpace(primary.match.RuleID),
			"host":    destination,
			"shape":   strings.TrimSpace(primary.match.GenericShape),
		},
	}
	tools.AttachUserGuidance(reject, resolution.Guidance)
	return nil, mcpRedactionReceipt{}, reject
}

// Screen failures carry no user guidance.
func mcpSecretFaultReject(destination string, primary mcpArgHit, err error) *tools.ToolReject {
	stage := secretmatch.FaultStageRaise
	if fault, ok := secretmatch.Faulted(err); ok {
		stage = fault.Stage
	}
	return &tools.ToolReject{
		Code: tools.OutboundSecretScreenFailedCode,
		Data: map[string]any{
			"surface":     string(secretmatch.SurfaceMCP),
			"rule_id":     strings.TrimSpace(primary.match.RuleID),
			"host":        destination,
			"shape":       strings.TrimSpace(primary.match.GenericShape),
			"fault_stage": stage,
		},
	}
}

func mcpSecretFingerprints(hits []mcpArgHit) []secretmatch.SecretFingerprint {
	matches := make([]secretmatch.Match, 0, len(hits))
	for _, hit := range hits {
		matches = append(matches, hit.match)
	}
	return secretmatch.Fingerprints(matches)
}

type mcpArgHit struct {
	value string
	match secretmatch.Match
	path  string
}

func mergeMCPSecretEvidence(known []secretmatch.Match, detected []mcpArgHit) []mcpArgHit {
	out := make([]mcpArgHit, 0, len(known)+len(detected))
	seen := map[secretmatch.SecretFingerprint]bool{}
	for _, match := range known {
		out = append(out, mcpArgHit{path: "arguments", match: match})
		seen[match.Fingerprint] = true
	}
	for _, hit := range detected {
		if hit.match.Fingerprint != "" && seen[hit.match.Fingerprint] {
			continue
		}
		seen[hit.match.Fingerprint] = true
		out = append(out, hit)
	}
	return out
}

func scanMCPArgs(ctx context.Context, matcher *secretmatch.Matcher, args map[string]any) []mcpArgHit {
	var hits []mcpArgHit
	visitMCPArgStrings(ctx, matcher, args, "$", "", func(path, label, value string) {
		detected := matcher.ScreenLabeledContext(ctx, label, value)
		for _, match := range secretcap.ResolutionFrom(ctx).UnattributedMatches(value, detected, mcpOutboundArgument) {
			hits = append(hits, mcpArgHit{match: match, path: path, value: secretmatch.ReviewValue(value, match)})
		}
	})
	return hits
}

func visitMCPArgStrings(ctx context.Context, matcher *secretmatch.Matcher, value any, path, label string, visit func(path, label, value string)) {
	switch typed := value.(type) {
	case string:
		visit(path, label, typed)
	case []string:
		for i, item := range typed {
			visit(fmt.Sprintf("%s[%d]", path, i), label, item)
		}
	case []any:
		for i, item := range typed {
			visitMCPArgStrings(ctx, matcher, item, fmt.Sprintf("%s[%d]", path, i), label, visit)
		}
	case map[string]any:
		keys := make([]string, 0, len(typed))
		for key := range typed {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		for _, key := range keys {
			safeKey := matcherSafePathKey(ctx, matcher, key, visit)
			visitMCPArgStrings(ctx, matcher, typed[key], path+"."+safeKey, key, visit)
		}
	}
}

func matcherSafePathKey(ctx context.Context, matcher *secretmatch.Matcher, key string, visit func(path, label, value string)) string {
	// Screen keys without retaining them as provenance.
	visit("$[argument key]", "", key)
	return matcher.RedactString(ctx, key)
}

func primaryMCPArgHit(hits []mcpArgHit) mcpArgHit {
	primary := hits[0]
	for _, hit := range hits[1:] {
		if hit.match.Severity == "critical" && primary.match.Severity != "critical" {
			primary = hit
		}
	}
	return primary
}

func redactMCPArgs(ctx context.Context, matcher *secretmatch.Matcher, args map[string]any) map[string]any {
	redacted, _ := redactMCPArgValue(ctx, matcher, "", args).(map[string]any)
	return redacted
}

func redactMCPArgValue(ctx context.Context, matcher *secretmatch.Matcher, label string, value any) any {
	switch typed := value.(type) {
	case string:
		return matcher.RedactLabeled(ctx, label, typed)
	case []string:
		out := append([]string(nil), typed...)
		for i := range out {
			out[i] = matcher.RedactLabeled(ctx, label, out[i])
		}
		return out
	case []any:
		out := append([]any(nil), typed...)
		for i := range out {
			out[i] = redactMCPArgValue(ctx, matcher, label, out[i])
		}
		return out
	case map[string]any:
		out := make(map[string]any, len(typed))
		for key, item := range typed {
			out[matcher.RedactString(ctx, key)] = redactMCPArgValue(ctx, matcher, key, item)
		}
		return out
	default:
		return value
	}
}

func (r *RegistryImpl) breakerFor(providerID string) *gobreaker.CircuitBreaker {
	r.mu.Lock()
	defer r.mu.Unlock()
	if br, ok := r.breakers[providerID]; ok {
		return br
	}
	br := gobreaker.NewCircuitBreaker(gobreaker.Settings{
		Name:        "mcp:" + providerID,
		MaxRequests: 1,
		Interval:    30 * time.Second,
		Timeout:     60 * time.Second,
		ReadyToTrip: func(counts gobreaker.Counts) bool {
			return counts.ConsecutiveFailures >= r.breakerTh
		},
		// Structured tool errors do not count as transport failures.
		IsSuccessful: func(err error) bool {
			if err == nil {
				return true
			}
			return tools.AsToolReject(err) != nil
		},
	})
	r.breakers[providerID] = br
	return br
}

func mcpOutboundArgument(path string) bool { return path != "/unredact" }
