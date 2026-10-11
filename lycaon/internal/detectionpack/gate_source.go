package detectionpack

import (
	"crypto/sha256"
	"encoding/base64"
	"strings"

	"github.com/lycaon/lycaon/internal/commandsurface"
	"github.com/lycaon/lycaon/internal/gate"
	"github.com/lycaon/lycaon/internal/hitl"
)

// GateSource adapts detection matching to the approval gate.
type GateSource struct {
	m         *Matcher
	semantics *ActionSemanticsCatalog
}

// NewGateSource wraps m. A nil matcher yields a source that never matches.
func NewGateSource(m *Matcher, semantics ...*ActionSemanticsCatalog) *GateSource {
	source := &GateSource{m: m}
	if len(semantics) > 0 {
		source.semantics = semantics[0]
	}
	return source
}

// MatchAction returns the highest-severity match for the posture.
func (s *GateSource) MatchAction(action hitl.ProposedAction, posture gate.Posture) (hitl.DetectionMatch, bool) {
	if s == nil || s.m == nil {
		return hitl.DetectionMatch{}, false
	}
	eval := EvaluateToolExec(s.m, s.observationFor(action))
	if !eval.OK {
		return hitl.DetectionMatch{}, false
	}
	hit := eval.Match
	if !Escalates(hit.Level, posture) {
		return hitl.DetectionMatch{}, false
	}
	return hitl.DetectionMatch{
		PackID:        hit.PackID,
		RuleID:        hit.RuleID,
		RuleTitle:     hit.RuleTitle,
		Level:         string(hit.Level),
		External:      hit.External,
		Local:         hit.Local,
		Unrecoverable: hit.Unrecoverable,
		Tagged:        hit.Tagged,
		CorrelationID: correlationID(SourceToolExec, hit, action.Scope.ChatSession(), action.Scope.ProjectDir, action.Invocation.Tool),
	}, true
}

// observationFor builds the synthetic event matched by detection rules.
func (s *GateSource) observationFor(action hitl.ProposedAction) ActionObservation {
	rawCommand := CommandTextForTool(action.Invocation.Tool, action.Invocation.Args)
	semantics := s.semantics.Resolve(action.Invocation.Tool, action.Resources.ApprovalCategory, action.Resources.ApprovalSubject, action.Invocation.Args)
	sockets := make([]SocketObservation, 0, len(action.Sockets.SocketGrants))
	for i, grant := range action.Sockets.SocketGrants {
		observation := SocketObservation{
			ApprovedPath:       grant.ApprovedPath,
			ResolvedPath:       grant.ResolvedPath,
			EffectiveAuthority: "outside_sandbox_daemon",
		}
		if i < len(action.Sockets.SocketScopes) {
			observation.Scope = action.Sockets.SocketScopes[i]
		}
		if i < len(action.Sockets.SocketGrantStates) {
			observation.GrantState = action.Sockets.SocketGrantStates[i]
		}
		sockets = append(sockets, observation)
	}
	// Structured argv preserves the executor's process boundaries.
	plan, _ := commandsurface.PlanGroups(action.Invocation.Args)
	roots := effectReachRoots(action.Execution.Contained)
	processes := commandProcesses(rawCommand, plan)
	processReach := make([]string, 0, len(processes))
	for _, process := range processes {
		processReach = append(processReach, resolveCommandEffectReach(
			action.Invocation.Tool, process.commandLine, action.Invocation.Args, action.Scope.ProjectDir, roots,
		))
	}
	evObs := ActionObservation{
		Tool:                 action.Invocation.Tool,
		CommandLine:          rawCommand,
		Plan:                 plan,
		ProjectDir:           action.Scope.ProjectDir,
		SessionID:            action.Scope.ChatSession(),
		ActionID:             action.Invocation.ActionID,
		Boundary:             action.Execution.Contained,
		Sockets:              sockets,
		Visibility:           action.Egress.Visibility,
		DeclaredDestinations: action.Egress.DeclaredDestinations,
		// Structured calls project operations into the shared API-action field.
		APIActions:            append(semantics.APIActions, APIActionsFromStructuredArgs(action.Invocation.Args)...),
		ActionEffects:         semantics.ActionEffects,
		TargetScopes:          semantics.TargetScopes,
		PrincipalScopes:       semantics.PrincipalScopes,
		CredentialPersistence: semantics.CredentialPersistence,
		BulkAction:            semantics.BulkAction,
		AmountPresent:         semantics.AmountPresent,
		TargetPresent:         semantics.TargetPresent,
		// Structured tools expose arguments directly to rules.
		ToolArgs:           projectToolArgs(action.Invocation.Args),
		TargetFiles:        action.Invocation.Files,
		EffectReach:        ResolveEffectReach(action.Invocation.Tool, action.Invocation.Args, action.Scope.ProjectDir, roots),
		ProcessEffectReach: processReach,
	}
	return evObs
}

// MintedCredentialRule reports the rule that identified credential issuance.
func (s *GateSource) MintedCredentialRule(action hitl.ProposedAction) (hitl.DetectionMatch, bool) {
	if s == nil || s.m == nil {
		return hitl.DetectionMatch{}, false
	}
	hit, ok := s.m.MatchMint(NewEvent(s.observationFor(action)))
	if !ok {
		return hitl.DetectionMatch{}, false
	}
	return hitl.DetectionMatch{
		PackID:    hit.PackID,
		RuleID:    hit.RuleID,
		RuleTitle: hit.RuleTitle,
		Level:     string(hit.Level),
		Tagged:    hit.Tagged,
	}, true
}

// effectReachRoots returns roots proven by the applied boundary.
func effectReachRoots(contained hitl.Contained) []string {
	if len(contained.WriteRoots) > 0 {
		return contained.WriteRoots
	}
	return contained.Roots
}

// Escalates reports whether the match applies to the posture.
func (s *GateSource) Escalates(match hitl.DetectionMatch, posture gate.Posture) bool {
	return Escalates(Level(match.Level), posture)
}

// EgressSource adapts a Matcher to confine.EgressDetectionSource via citation fields.
type EgressSource struct{ m *Matcher }

// NewEgressSource wraps m for CONNECT-hold matching.
func NewEgressSource(m *Matcher) *EgressSource {
	return &EgressSource{m: m}
}

// Match builds an EgressEvent and returns the highest-severity egress match.
func (s *EgressSource) Match(observation EgressObservation, image, rawCommand string) (hitl.DetectionMatch, bool) {
	if s == nil || s.m == nil {
		return hitl.DetectionMatch{}, false
	}
	images := newCommandEvent("", rawCommand, "", true, "proxy", observation.SessionID, nil).Image
	if image = strings.TrimSpace(image); image != "" {
		images = append([]string{image}, images...)
	}
	observation.Image = uniqueStrings(append(observation.Image, images...))
	ev := NewEgressEvent(observation)
	hit, ok := s.m.MatchEgress(ev)
	if !ok {
		return hitl.DetectionMatch{}, false
	}
	return hitl.DetectionMatch{
		PackID:        hit.PackID,
		RuleID:        hit.RuleID,
		RuleTitle:     hit.RuleTitle,
		Level:         string(hit.Level),
		External:      hit.External,
		Local:         hit.Local,
		Unrecoverable: hit.Unrecoverable,
		Tagged:        hit.Tagged,
		CorrelationID: correlationID(SourceEgressObserved, hit, ev.SessionID, ev.DestinationHostname),
	}, true
}

func correlationID(source LogSource, hit Match, subjects ...string) string {
	parts := []string{string(source), hit.PackID, hit.RuleID, string(hit.Level)}
	parts = append(parts, subjects...)
	digest := sha256.Sum256([]byte(strings.Join(parts, "\x00")))
	return "det_" + base64.RawURLEncoding.EncodeToString(digest[:16])
}

func uniqueStrings(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	out := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		out = append(out, value)
	}
	return out
}

// Escalates reports whether the match applies to the posture.
func (s *EgressSource) Escalates(match hitl.DetectionMatch, posture gate.Posture) bool {
	return Escalates(Level(match.Level), posture)
}
