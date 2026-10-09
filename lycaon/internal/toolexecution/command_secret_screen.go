package toolexecution

import (
	"context"
	"github.com/lycaon/lycaon/internal/toolrejection"
	"github.com/lycaon/lycaon/internal/tools"
	"sort"
	"strings"

	"github.com/lycaon/lycaon/internal/commandsurface"
	"github.com/lycaon/lycaon/internal/confine"
	"github.com/lycaon/lycaon/internal/hitl"
	"github.com/lycaon/lycaon/internal/secretcap"
	"github.com/lycaon/lycaon/internal/secretmatch"
	"github.com/lycaon/lycaon/internal/toolcontract"
)

// SetSecretMatcher installs argument screening.
func (e *Secrets) SetSecretMatcher(m *secretmatch.Matcher) {
	if e == nil {
		return
	}
	e.secretMatcher = m
}

// processArgumentSurface is the card identity for arguments handed to a host process.
func processArgumentSurface(contract toolcontract.Contract) (secretmatch.ScreenSurface, bool) {
	surface := contract.SecretReferenceSurface
	return secretmatch.ScreenSurface(surface), surface.ProcessArguments()
}

// screenArgvSecrets checks egress-capable arguments, and any carrying held
// values, before execution.
func (e *Secrets) screenArgvSecrets(
	ctx context.Context,
	tool string,
	args map[string]any,
	tc tools.ToolContext,
) error {
	if e == nil {
		return nil
	}
	surface, screened := processArgumentSurface(tc.Invocation.Contract)
	if !screened {
		return nil
	}
	// A value a person holds is screened wherever it goes, since the handoff
	// refuses it without a reviewed release.
	if !e.actionCanEgress(ctx, tc) && len(secretUseFrom(ctx)) == 0 && !tc.Secrets.HoldsPersonValues() {
		return nil
	}
	known, err := tc.Secrets.Matches(e.secretMatcher, func(string) bool { return true })
	if err != nil {
		return argvSecretFaultReject(ctx, e, tool, tc, surface, secretmatch.Match{}, secretmatch.NewAskFault(secretmatch.FaultStageScreenUnwired, err))
	}
	attribution := secretmatch.AskAttributionFrom(ctx)
	attribution.ProjectID = tc.ProjectID
	attribution.SessionID = tc.SessionID
	attribution.ToolCallID = tc.ToolCallID
	ctx = secretmatch.WithAskAttribution(ctx, attribution)
	classification := e.secretMatcher.ClassificationSnapshot(ctx)
	ctx = e.secretMatcher.WithClassifications(ctx, classification)
	raw := screenArgvSecretMatches(ctx, e.secretMatcher, args)
	for _, matches := range [][]secretmatch.Match{known, raw} {
		if len(matches) == 0 {
			continue
		}
		if err := e.screenArgvSecretGroup(ctx, tool, args, tc, surface, matches); err != nil {
			return err
		}
	}
	if err := e.secretMatcher.CheckClassification(ctx, classification); err != nil {
		return argvSecretFaultReject(ctx, e, tool, tc, surface, secretmatch.Match{}, err)
	}
	return nil
}

func (e *Secrets) screenArgvSecretGroup(ctx context.Context, tool string, args map[string]any, tc tools.ToolContext, surface secretmatch.ScreenSurface, matches []secretmatch.Match) error {
	match := matches[0]
	finding := argvSecretFinding(surface, tool, tc, match, matches)
	visitArgvStrings(ctx, args, "", func(label, value string) {
		for _, hit := range e.secretMatcher.ScreenLabeledContext(ctx, label, value) {
			if hit.Fingerprint == match.Fingerprint {
				finding.ReviewValue = secretmatch.ReviewValue(value, hit)
			}
		}
	})
	finding.SecretNames = secretmatch.ManagedNames(matches)
	if finding.Managed() {
		finding.Recipients = secretUseFrom(ctx)
		finding.ConnectPorts = secretUseConnectPorts(ctx)
		finding.RecipientsLocal = argvSecretRecipientsLocal(ctx)
	}
	if line := commandsurface.PrimaryCommandLine(args, nil); line != "" {
		finding.CommandLine = e.secretMatcher.RedactString(ctx, line)
	}
	return e.resolveArgumentSecretFinding(ctx, tool, tc, surface, match, finding)
}

// resolveArgumentSecretFinding asks about a finding, or applies recorded
// redaction decisions when approvals are disabled.
func (e *Secrets) resolveArgumentSecretFinding(
	ctx context.Context,
	tool string,
	tc tools.ToolContext,
	surface secretmatch.ScreenSurface,
	match secretmatch.Match,
	finding secretmatch.Alert,
) error {
	resolve := e.AskSecretScreen
	if e.Capabilities.approvalsDisabled != nil && e.Capabilities.approvalsDisabled(tc.ActiveRootPath()) {
		resolve = e.ResolveSecretScreenUnasked
	}
	resolution, err := resolve(ctx, finding)
	if err != nil {
		return argvSecretFaultReject(ctx, e, tool, tc, surface, match, err)
	}
	if !resolution.Decision.Blocks() {
		return nil
	}
	return argvSecretReject(ctx, e, tool, tc, surface, match, resolution)
}

// screenArgvSecretMatches preserves field labels and full field contents.
func screenArgvSecretMatches(ctx context.Context, matcher *secretmatch.Matcher, args map[string]any) []secretmatch.Match {
	var matches []secretmatch.Match
	visitArgvStrings(ctx, args, "", func(label, value string) {
		detected := matcher.ScreenLabeledContext(ctx, label, value)
		matches = append(matches, secretcap.ResolutionFrom(ctx).UnattributedMatches(value, detected, nil)...)
	})
	return matches
}

func visitArgvStrings(ctx context.Context, value any, label string, visit func(label, value string)) {
	if ctx == nil || ctx.Err() != nil {
		return
	}
	switch typed := value.(type) {
	case string:
		visit(label, typed)
	case []string:
		for _, item := range typed {
			visit(label, item)
		}
	case []any:
		for _, item := range typed {
			visitArgvStrings(ctx, item, label, visit)
		}
	case map[string]any:
		keys := make([]string, 0, len(typed))
		for key := range typed {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		for _, key := range keys {
			visit("", key)
			visitArgvStrings(ctx, typed[key], key, visit)
		}
	}
}

// actionCanEgress uses the executor's confinement request.
func (e *Secrets) actionCanEgress(ctx context.Context, tc tools.ToolContext) bool {
	req := e.Boundary.actionConfineRequest(ctx, tc)
	if tc.DirectIPAuthorized && req.Egress != confine.EgressDirectIP {
		req.Egress = confine.EgressDirectIP
		req.SocksProxyEnv = false
	}
	return hitl.ContainedForRequest(req).Egress != hitl.ContainedEgressDeny
}

func argvSecretFinding(
	surface secretmatch.ScreenSurface,
	tool string,
	tc tools.ToolContext,
	match secretmatch.Match,
	matches []secretmatch.Match,
) secretmatch.Alert {
	destination, label := argvSecretDestination(tc)
	return secretmatch.Alert{
		SessionID:        tc.SessionID,
		RootSessionID:    tc.ChatSessionID(),
		ProjectID:        tc.ProjectID,
		ProjectDir:       tc.ActiveRootPath(),
		ToolCallID:       tc.ToolCallID,
		Surface:          surface,
		DestinationID:    destination,
		DestinationLabel: label,
		RuleID:           match.RuleID,
		RuleTitle:        match.Title,
		GenericShape:     match.GenericShape,
		VarName:          match.VarName,
		Container:        match.Container,
		Occurrences:      len(matches),
		SourceKind:       secretmatch.SourceToolArgument,
		SourceTool:       tool,
		SourcePath:       "arguments",
		OriginKind:       secretmatch.OriginField,
		Fingerprints:     secretmatch.Fingerprints(matches),
	}
}

// argvSecretDestination describes the observable egress boundary.
func argvSecretDestination(tc tools.ToolContext) (string, string) {
	if tc.DirectIPAuthorized {
		return string(hitl.ContainedEgressDirectIP), "processes in this chat with direct network access"
	}
	return string(hitl.ContainedEgressProxy), "processes in this chat with mediated network access"
}

func stringArgOrEmpty(args map[string]any, key string) string {
	if args == nil {
		return ""
	}
	s, _ := args[key].(string)
	return strings.TrimSpace(s)
}

func argvSecretReject(
	ctx context.Context,
	e *Secrets,
	tool string,
	tc tools.ToolContext,
	surface secretmatch.ScreenSurface,
	match secretmatch.Match,
	resolution secretmatch.Resolution,
) error {
	tc.Secrets.Withhold(ctx)
	_, destination := fileSecretDestination(stringArgOrEmpty(tc.CanonicalArgs, "path"))
	if surface != secretmatch.SurfaceFile {
		_, destination = argvSecretDestination(tc)
	}
	reject := &toolrejection.ToolReject{
		Code: toolrejection.OutboundSecretDeniedCode,
		Data: map[string]any{
			"surface": string(surface),
			"rule_id": strings.TrimSpace(match.RuleID),
			"shape":   strings.TrimSpace(match.GenericShape),
			"host":    destination,
		},
	}
	toolrejection.AttachUserGuidance(reject, resolution.Guidance)
	return e.Rejections.rejectBeforeInvoke(ctx, tool, tc.Agent, nil, reject)
}

// argvSecretFaultReject distinguishes host faults from human decisions.
func argvSecretFaultReject(
	ctx context.Context,
	e *Secrets,
	tool string,
	tc tools.ToolContext,
	surface secretmatch.ScreenSurface,
	match secretmatch.Match,
	err error,
) error {
	stage := secretmatch.FaultStageRaise
	if fault, ok := secretmatch.Faulted(err); ok {
		stage = fault.Stage
	}
	return e.Rejections.rejectBeforeInvoke(ctx, tool, tc.Agent, nil, &toolrejection.ToolReject{
		Code: toolrejection.OutboundSecretScreenFailedCode,
		Data: map[string]any{
			"surface":     string(surface),
			"rule_id":     strings.TrimSpace(match.RuleID),
			"shape":       strings.TrimSpace(match.GenericShape),
			"fault_stage": stage,
		},
	})
}
