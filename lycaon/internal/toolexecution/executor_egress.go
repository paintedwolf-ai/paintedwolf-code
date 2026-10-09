package toolexecution

import (
	"context"
	"github.com/lycaon/lycaon/internal/destconfig"
	"github.com/lycaon/lycaon/internal/pkgregistry"
	"strings"

	"github.com/lycaon/lycaon/internal/authzledger"
	"github.com/lycaon/lycaon/internal/confine"
	"github.com/lycaon/lycaon/internal/egress"
	"github.com/lycaon/lycaon/internal/egressgate"
	"github.com/lycaon/lycaon/internal/egressproxy"
	"github.com/lycaon/lycaon/internal/evidence"
	"github.com/lycaon/lycaon/internal/gate"
	"github.com/lycaon/lycaon/internal/hitl"
)

// configuredBy names the user configuration that declares a host.
func (e *Network) configuredBy(projectDir, host string) string {
	if e == nil || e.destinations == nil {
		return ""
	}
	name, _ := e.destinations.Configured(projectDir, host)
	return name
}

// publicRegistry names the catalogued public package registry serving host.
func (e *Network) publicRegistry(host string) string {
	if e == nil {
		return ""
	}
	registry, _ := e.registries.ForHost(host)
	return registry.Title
}

// resolveEgress gates an observed destination before dialing. Decision errors deny the dial.
func (e *Network) resolveEgress(ctx context.Context, cmd confine.EgressCommand, ep egressproxy.Endpoint, detection *confine.EgressDetectionCitation) bool {
	if e == nil || e.Approvals.checkpointMgr == nil {
		return false
	}
	ctx = authzledger.WithInvocation(ctx, cmd.SessionID, cmd.RootSessionID, cmd.ToolCallID)
	subject := egressAskFor(cmd, ep, detection)
	action := hitl.ProposedAction{
Invocation: hitl.ActionInvocation{
Tool: networkEgressTool,
Args: subject.Args,
},
Presentation: hitl.ActionPresentation{
PresentationTool: strings.TrimSpace(cmd.ToolName),
Command: strings.TrimSpace(cmd.CommandLine),
EstimatedImpact: subject.Impact,
},
Scope: hitl.ActionScope{
ProjectID: cmd.ProjectID,
ProjectDir: cmd.ProjectDir,
SessionID: cmd.SessionID,
RootSessionID: cmd.RootSessionID,
},
Execution: hitl.ActionExecution{
Contained: subject.Contained,
},
}

	facts := gate.Facts{
		Stage: gate.StagePreDial,
		Ran: gate.ProducerContainment | gate.ProducerDestination |
			gate.ProducerLease | gate.ProducerRule | gate.ProducerDetection,
		Containment: gate.Containment{FSJailed: true, Egress: gate.EgressProxy},
		Destination: destinationFact(cmd, ep,
			e.firstUseThisSession(action.Scope.ChatSession(), ep.Host), e.configuredBy(cmd.ProjectDir, ep.Host), e.publicRegistry(ep.Host)),
		Leased:           e.egressLeaseCovers(ctx, action) || e.loopbackLeaseCovers(ctx, cmd, ep),
		RequestConsented: cmd.ToolName == "http_request" && egressgate.RequestConsented(ctx, ep.Host, ep.Port),
	}
	if cmd.UserRule != nil {
		facts.UserRule = &gate.UserRule{
			Category: cmd.UserRule.Category,
			Pattern:  cmd.UserRule.Pattern,
			Subject:  cmd.UserRule.Subject,
		}
	}
	var approvalMatches []hitl.ApprovalRuleMatch
	if cmd.UserRule != nil {
		approvalMatches = []hitl.ApprovalRuleMatch{{
			Category: cmd.UserRule.Category, Pattern: cmd.UserRule.Pattern, Effect: "ask",
			UnitID: cmd.UserRule.UnitID, PackID: cmd.UserRule.PackID, Scope: cmd.UserRule.Scope,
		}}
	}
	if e.Secrets.secretExposure != nil {
		exposed, err := e.Secrets.secretExposure(ctx, action.Scope.ChatSession())
		if err == nil {
			facts.Ran |= gate.ProducerExposure
			facts.SecretExposed = exposed
		}
	}
	if e.Secrets.untrustedIngestion != nil {
		ingested, err := e.Secrets.untrustedIngestion(ctx, action.Scope.ChatSession())
		if err == nil {
			facts.Ran |= gate.ProducerIngestion
			facts.UntrustedIngested = ingested
		}
	}
	var det *hitl.DetectionMatch
	if detection != nil {
		det = &hitl.DetectionMatch{
			PackID:        detection.PackID,
			RuleID:        detection.RuleID,
			RuleTitle:     detection.RuleTitle,
			Level:         detection.Level,
			External:      detection.External,
			Local:         detection.Local,
			Unrecoverable: detection.Unrecoverable,
			Tagged:        detection.Tagged,
			CorrelationID: detection.CorrelationID,
		}
		// Authority-misuse rules apply to both local and external effects.
		facts.Detection = &gate.Match{
			PackID: det.PackID, RuleID: det.RuleID, RuleTitle: det.RuleTitle, Level: det.Level,
			External: det.External || !det.Tagged, Local: det.Local,
			Unrecoverable: det.Unrecoverable || !det.Tagged,
		}
	}

	verdict, decision := gate.Evaluate(facts, e.egressPosture(cmd))
	if verdict != gate.Ask {
		e.recordHostVisit(ctx, action.Scope.ChatSession(), ep.Host)
		return true
	}

	raise := egressRaise{
		cmd: cmd, ep: ep, action: action, subject: subject, decision: decision, facts: facts,
		detection: det, matches: approvalMatches,
	}
	if allowed, handled := e.gatherEgress(ctx, raise); handled {
		return allowed
	}
	final := e.raiseEgressCard(ctx, raise)
	if final == nil {
		return false
	}
	allowed := hitl.CheckpointAuthorizes(final)
	e.settleEgressMember(ctx, raise, allowed, final)
	return allowed
}

// firstUseThisSession treats a missing ledger as first use, preserving the approval requirement.
func (e *Network) firstUseThisSession(chatSessionID, host string) bool {
	if e == nil || e.hostLedger == nil {
		return true
	}
	return !evidence.HostVisited(e.hostLedger.SessionVisitedHosts(chatSessionID), host)
}

// recordHostVisit records only destinations whose dial was allowed.
func (e *Network) recordHostVisit(ctx context.Context, chatSessionID, host string) {
	if e == nil || e.hostLedger == nil {
		return
	}
	_ = e.hostLedger.RecordHostVisit(ctx, chatSessionID, host)
}

// egressLeaseCovers is true only when a chat or durable grant covers the destination.
func (e *Network) egressLeaseCovers(_ context.Context, action hitl.ProposedAction) bool {
	if e == nil || e.Approvals.approvalGate == nil {
		return false
	}
	return e.Approvals.approvalGate.GrantCovers(action)
}

// loopbackLeaseCovers accepts address literals; the broker checks the resolved address before dialing.
func (e *Network) loopbackLeaseCovers(ctx context.Context, cmd confine.EgressCommand, ep egressproxy.Endpoint) bool {
	if e == nil || e.Boundary.sessionLoopbackGrant == nil || !egress.LoopbackLiteral(ep.Host) {
		return false
	}
	granted, ports := e.Boundary.sessionLoopbackGrant(ctx, cmd.SessionID, cmd.RootSessionID)
	return granted && axisPortsCovered(ports, []uint16{ep.Port})
}

// egressPosture reads the ask-line that applies to the attributed project.
func (e *Network) egressPosture(cmd confine.EgressCommand) gate.Posture {
	if e.egressPostureFor == nil {
		return gate.DefaultPosture
	}
	return e.egressPostureFor(cmd.ProjectDir)
}

func (e *Network) SetSessionHostLedger(l SessionHostLedger) {
	if e != nil {
		e.hostLedger = l
	}
}

func (e *Network) SetDestinationConfig(r *destconfig.Registry) {
	if e != nil {
		e.destinations = r
	}
}

func (e *Network) SetPackageRegistries(c *pkgregistry.Catalog) {
	if e != nil {
		e.registries = c
	}
}

func (e *Network) SetEgressPostureSource(fn func(projectDir string) gate.Posture) {
	if e != nil {
		e.egressPostureFor = fn
	}
}
