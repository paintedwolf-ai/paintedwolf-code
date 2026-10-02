package gate

import (
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/lycaon/lycaon/pkg/api"
)

// Verdict reports whether an action proceeds silently or requires review.
type Verdict string

const (
	// Silent means no live gate fired. The action proceeds and is recorded.
	Silent Verdict = "silent"
	// Ask means at least one live gate fired, or a producer that should have
	// reported did not.
	Ask Verdict = "ask"
)

// Decision combines all approval reasons for one action.
type Decision struct {
	Primary api.ApprovalGate
	Also    []api.ApprovalGate
	// Cited is every input that made this fire. A Decision with none is invalid.
	Cited []Fact
	// ReasonKey groups repeat diagnostics independently of authority.
	ReasonKey string
	// Posture is the ask-line this decision was evaluated under. Downstream
	// ladder shaping reads it here so the card and the verdict share one input.
	Posture Posture
}

// Gates returns the primary gate followed by the others that fired.
func (d *Decision) Gates() []api.ApprovalGate {
	if d == nil {
		return nil
	}
	return append([]api.ApprovalGate{d.Primary}, d.Also...)
}

// ReasonSegments returns each gate:reason piece that ReasonKey joins with "|".
// Quiet keys one segment; suppress only when every segment is quieted.
func (d *Decision) ReasonSegments() []string {
	if d == nil || strings.TrimSpace(d.ReasonKey) == "" {
		return nil
	}
	parts := strings.Split(d.ReasonKey, "|")
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part != "" {
			out = append(out, part)
		}
	}
	return out
}

// Reuse intersects the authority shapes and scopes permitted by every active reason.
func (d *Decision) Reuse() Reuse {
	if d == nil {
		return Reuse{}
	}
	out := Reuse{Shape: ReusePredicate, Scope: ScopeDevice, DayCarrier: ScopeDevice}
	for _, cited := range d.Cited {
		if cited.Key == "boundary.execution" {
			out = Reuse{Shape: ReusePredicate, Scope: ScopeChat, DayCarrier: ScopeChat}
		}
	}
	for _, g := range d.Gates() {
		r := ReuseFor(g)
		if !r.Offered() {
			return Reuse{}
		}
		if shapeRank(r.Shape) < shapeRank(out.Shape) {
			out.Shape = r.Shape
		}
		if scopeRank(r.Scope) < scopeRank(out.Scope) {
			out.Scope = r.Scope
		}
		// The time limit also respects the narrowest permitted scope.
		if scopeRank(r.DayScope()) < scopeRank(out.DayCarrier) {
			out.DayCarrier = r.DayScope()
		}
	}
	return out
}

func shapeRank(s ReuseShape) int {
	switch s {
	case ReuseExactAction:
		return 1
	case ReusePredicate:
		return 2
	default:
		return 0
	}
}

func scopeRank(s Scope) int {
	switch s {
	case ScopeChat:
		return 1
	case ScopeProject:
		return 2
	case ScopeDevice:
		return 3
	default:
		return 0
	}
}

// definition binds a gate to its stages, required producers, and predicate.
type definition struct {
	gate             api.ApprovalGate
	stages           []Stage
	when             func(Facts) bool
	requires         Producer
	predicate        func(Facts) (bool, []Fact, string)
	posturePredicate func(Facts, Posture) (bool, []Fact, string)
}

func definitions() []definition {
	return []definition{
		{
			gate:      api.GateAgentPolicyChange,
			stages:    []Stage{StagePreSpawn},
			when:      func(f Facts) bool { return len(f.AgentPolicyPaths) > 0 },
			requires:  ProducerFilePath,
			predicate: agentPolicyChange,
		},
		{
			gate:             api.GateSecretOutbound,
			stages:           []Stage{StagePreSend},
			when:             ordinaryAction,
			requires:         ProducerPayload,
			posturePredicate: secretOutbound,
		},
		{
			gate:      api.GateConsentDrift,
			stages:    []Stage{StagePreSpawn},
			when:      ordinaryAction,
			requires:  ProducerConsent,
			predicate: consentDrift,
		},
		{
			gate:      api.GateRemotePackageExecution,
			stages:    []Stage{StagePreSpawn},
			when:      func(f Facts) bool { return f.PackageExecution != nil },
			requires:  ProducerPackageExecution | ProducerLease,
			predicate: remotePackageExecution,
		},
		{
			gate:      api.GateRemotePackageExecutionKnown,
			stages:    []Stage{StagePreSpawn},
			when:      func(f Facts) bool { return f.PackageExecution != nil },
			requires:  ProducerPackageExecution | ProducerLease,
			predicate: remotePackageExecutionKnown,
		},
		{
			gate:      api.GateUnobservedChannel,
			stages:    []Stage{StagePreSpawn},
			when:      ordinaryAction,
			requires:  ProducerContainment | ProducerLease,
			predicate: unobservedChannel,
		},
		{
			gate:      api.GateAuthorityMisuse,
			stages:    []Stage{StagePreSpawn, StagePreDial},
			when:      ordinaryAction,
			requires:  ProducerDetection | ProducerContainment,
			predicate: authorityMisuse,
		},
		{
			gate:      api.GateSensitiveLocation,
			stages:    []Stage{StagePreSpawn},
			when:      ordinaryAction,
			requires:  ProducerFilePath | ProducerLease,
			predicate: sensitiveLocation,
		},
		{
			gate:      api.GateOutsideRootsWrite,
			stages:    []Stage{StagePreSpawn},
			when:      ordinaryAction,
			requires:  ProducerFilePath | ProducerLease,
			predicate: outsideRootsWrite,
		},
		{
			gate:      api.GateOutsideRootsRead,
			stages:    []Stage{StagePreSpawn},
			when:      ordinaryAction,
			requires:  ProducerFilePath | ProducerLease,
			predicate: outsideRootsRead,
		},
		{
			gate:   api.GateSecretExposedOutbound,
			stages: []Stage{StagePreDial},
			when:   ordinaryAction,
			// Requiring the exposure producer means an unwired deployment raises
			// incomplete_facts.
			requires:  ProducerDestination | ProducerExposure | ProducerLease,
			predicate: secretExposedOutbound,
		},
		{
			gate:             api.GateAgentChosenOutbound,
			stages:           []Stage{StagePreDial},
			when:             ordinaryAction,
			requires:         ProducerDestination | ProducerLease,
			posturePredicate: agentChosenOutbound,
		},
		{
			gate:      api.GateFirstHost,
			stages:    []Stage{StagePreDial},
			when:      ordinaryAction,
			requires:  ProducerDestination | ProducerLease,
			predicate: firstHost,
		},
		{
			gate:      api.GateMCPUnleased,
			stages:    []Stage{StagePreSpawn},
			when:      ordinaryAction,
			requires:  ProducerLease,
			predicate: mcpUnleased,
		},
		{
			gate: api.GateUserRule,
			// Destination ask rules are known only once the mediated endpoint exists;
			// deny remains enforcement in the egress broker.
			stages:    []Stage{StagePreSpawn, StagePreDial},
			when:      ordinaryAction,
			requires:  ProducerRule | ProducerLease,
			predicate: userRule,
		},
		{
			gate:      api.GateExplicitApprovalRequest,
			stages:    []Stage{StagePreSpawn},
			when:      func(f Facts) bool { return f.ApprovalRequest != nil && f.CapabilityWidening == nil },
			requires:  ProducerApprovalRequest,
			predicate: explicitApprovalRequest,
		},
		{
			gate:             api.GateCapabilityWidening,
			stages:           []Stage{StagePreSpawn},
			when:             func(f Facts) bool { return f.CapabilityWidening != nil },
			requires:         ProducerApprovalRequest,
			posturePredicate: capabilityWidening,
		},
	}
}

func packageExecutionKnown(execution *PackageExecution) bool {
	if execution == nil || len(execution.Packages) == 0 {
		return false
	}
	for _, pkg := range execution.Packages {
		if pkg.Status != "resolved" || pkg.AgeDays == nil || *pkg.AgeDays < 7 {
			return false
		}
	}
	return true
}

func packageExecutionFacts(execution *PackageExecution) (bool, []Fact, string) {
	cited := []Fact{
		fact("package.manager", execution.Manager, "package_execution_catalog"),
		fact("package.operation", execution.Operation, "package_execution_catalog"),
		fact("boundary.ambient_secrets", "removed", "confinement"),
		fact("boundary.egress", "registry-only without another decision", "confinement"),
	}
	for _, pkg := range execution.Packages {
		cited = append(cited, fact("package.system", pkg.System, "registry_identity"))
		coordinate := strings.TrimSpace(pkg.Name)
		if version := strings.TrimSpace(pkg.Version); version != "" {
			coordinate += "@" + version
		}
		cited = append(cited, fact("package.coordinate", coordinate, "registry_identity"))
		if pkg.AgeDays != nil {
			cited = append(cited, fact("package.age_days", strconv.Itoa(*pkg.AgeDays), "registry_identity"))
		}
		cited = append(cited, fact("package.identity_status", pkg.Status, "registry_identity"))
		if pkg.SourceRepository != "" {
			cited = append(cited, fact("package.source_repository", pkg.SourceRepository, "registry_identity"))
		}
		if pkg.VerifiedAttestation {
			cited = append(cited, fact("package.attestation", "verified", "registry_identity"))
		}
	}
	return true, cited, execution.Manager + ":" + execution.Operation
}

func remotePackageExecution(f Facts) (bool, []Fact, string) {
	if f.PackageExecution == nil || f.LeasedExact || f.LeasedPackage {
		return false, nil, ""
	}
	if packageExecutionKnown(f.PackageExecution) {
		return false, nil, ""
	}
	return packageExecutionFacts(f.PackageExecution)
}

func remotePackageExecutionKnown(f Facts) (bool, []Fact, string) {
	if f.PackageExecution == nil || f.LeasedExact || f.LeasedPackage {
		return false, nil, ""
	}
	if !packageExecutionKnown(f.PackageExecution) {
		return false, nil, ""
	}
	return packageExecutionFacts(f.PackageExecution)
}

// ordinaryAction is true when this evaluation is not a typed review or a capability widening.
func ordinaryAction(f Facts) bool {
	return f.ApprovalRequest == nil && f.CapabilityWidening == nil
}

func (d definition) appliesAt(s Stage) bool {
	for _, stage := range d.stages {
		if stage == s {
			return true
		}
	}
	return false
}

// Evaluate combines supplied facts. Missing required reports raise GateIncompleteFacts.
func Evaluate(f Facts, p Posture) (Verdict, *Decision) {
	var fired []api.ApprovalGate
	citedByGate := make(map[api.ApprovalGate][]Fact)
	reasonByGate := make(map[api.ApprovalGate]string)
	var missing []Fact

	for _, d := range definitions() {
		if !d.appliesAt(f.Stage) || !p.Enables(d.gate) {
			continue
		}
		if d.when != nil && !d.when(f) {
			continue
		}
		if !f.Ran.Has(d.requires) {
			missing = append(missing, fact("gate", string(d.gate), "evaluate"))
			continue
		}
		var ok bool
		var facts []Fact
		var reason string
		if d.posturePredicate != nil {
			ok, facts, reason = d.posturePredicate(f, p)
		} else if d.predicate != nil {
			ok, facts, reason = d.predicate(f)
		}
		if !ok {
			continue
		}
		fired = append(fired, d.gate)
		citedByGate[d.gate] = append(citedByGate[d.gate], facts...)
		reasonByGate[d.gate] = reason
	}

	if len(missing) > 0 {
		missing = append(missing, fact("stage", string(f.Stage), "evaluate"))
		missing = stampFacts(api.GateIncompleteFacts, missing)
		// The reason key is a constant so hitl scopes this gate's quiet to the
		// exact action; a quiet on "the host cannot tell" cannot generalise.
		return Ask, &Decision{
			Primary:   api.GateIncompleteFacts,
			Cited:     missing,
			ReasonKey: string(api.GateIncompleteFacts) + ":unreported",
			Posture:   normalize(p),
		}
	}
	if len(fired) == 0 {
		return Silent, nil
	}

	sort.SliceStable(fired, func(i, j int) bool { return rank(fired[i]) < rank(fired[j]) })
	cited := make([]Fact, 0)
	var reasons []string
	for _, firedGate := range fired {
		cited = append(cited, stampFacts(firedGate, citedByGate[firedGate])...)
		if reason := reasonByGate[firedGate]; reason != "" {
			reasons = append(reasons, string(firedGate)+":"+reason)
		}
	}
	return Ask, &Decision{
		Primary:   fired[0],
		Also:      fired[1:],
		Cited:     cited,
		ReasonKey: strings.Join(reasons, "|"),
		Posture:   normalize(p),
	}
}

func stampFacts(gate api.ApprovalGate, facts []Fact) []Fact {
	out := make([]Fact, len(facts))
	copy(out, facts)
	for i := range out {
		out[i].Gate = gate
	}
	return out
}

func agentPolicyChange(f Facts) (bool, []Fact, string) {
	if f.AgentPolicyLeased {
		return false, nil, ""
	}
	cited := make([]Fact, 0, len(f.AgentPolicyPaths))
	for _, path := range f.AgentPolicyPaths {
		cited = append(cited, fact("file.path", path, "agent_policy_target"))
	}
	return len(cited) > 0, cited, "project_agent_policy"
}

func explicitApprovalRequest(f Facts) (bool, []Fact, string) {
	if f.ApprovalRequest == nil || f.ApprovalRequest.Count < 1 {
		return false, nil, ""
	}
	count := strconv.Itoa(f.ApprovalRequest.Count)
	// Reason is a constant; ExactActionQuiet keys the quiet to this action.
	return true, []Fact{fact("actions.count", count, "approval_request")}, "requested"
}

func capabilityWidening(f Facts, p Posture) (bool, []Fact, string) {
	if f.CapabilityWidening == nil || len(f.CapabilityWidening.Axes) == 0 {
		return false, nil, ""
	}
	cw := f.CapabilityWidening
	if p == PostureBalanced {
		allSilent := true
		for _, axis := range cw.Axes {
			switch axis {
			case AxisLocalListen:
				// The listener boundary binds loopback only; the port decides.
				if hasPrivilegedPort(cw.Ports) {
					allSilent = false
				}
			case AxisLoopbackConnect:
				if !cw.SelfSpawned {
					allSilent = false
				}
			default:
				allSilent = false
			}
		}
		if allSilent {
			return false, nil, ""
		}
	}
	cited := make([]Fact, 0, len(cw.Axes))
	for _, axis := range cw.Axes {
		axis = strings.TrimSpace(axis)
		if axis == "" {
			continue
		}
		cited = append(cited, fact("capability.axis", axis, "capability_budget"))
	}
	if len(cited) == 0 {
		return false, nil, ""
	}
	return true, cited, strings.Join(cw.Axes, "+")
}

func hasPrivilegedPort(ports []uint16) bool {
	for _, port := range ports {
		if port > 0 && port < 1024 {
			return true
		}
	}
	return false
}

// secretOutbound reviews increased disclosure; public inbound content and trusted destinations are exempt.
func secretOutbound(f Facts, p Posture) (bool, []Fact, string) {
	if f.Payload == nil {
		return false, nil, ""
	}
	if !f.Payload.Held {
		if f.Payload.Source == SecretSourcePublicInbound && f.Payload.Surface != "visual_perception" {
			return false, nil, ""
		}
		if f.Payload.DestinationTrusted || p.ReleasesChatSecretLocally(f.Payload) {
			return false, nil, ""
		}
	}
	cited := []Fact{
		fact("secret.rule", firstNonEmpty(f.Payload.RuleTitle, f.Payload.RuleID), "payload_lens"),
		fact("secret.surface", f.Payload.Surface, "payload_lens"),
		fact("secret.source", secretSourceValue(f.Payload.Source), "payload_provenance"),
	}
	if f.Payload.SourceKind != "" {
		cited = append(cited, fact("secret.source_kind", f.Payload.SourceKind, "payload_provenance"))
	}
	if f.Payload.SourceTool != "" {
		cited = append(cited, fact("secret.source_tool", f.Payload.SourceTool, "payload_provenance"))
	}
	if f.Payload.Occurrences > 1 {
		cited = append(cited, fact("secret.occurrences", strconv.Itoa(f.Payload.Occurrences), "payload_lens"))
	}
	if f.Payload.ChatGenerated {
		cited = append(cited, fact("secret.origin", "generated for this chat", "managed_secret"))
	}
	if f.Payload.Held {
		cited = append(cited, fact("secret.custody", "a value you gave to Painted Wolf Code", "managed_secret"))
	}
	return true, cited, f.Payload.RuleID
}

func secretSourceValue(source SecretSource) string {
	if source == "" {
		return string(SecretSourceUnknown)
	}
	return string(source)
}

func consentDrift(f Facts) (bool, []Fact, string) {
	if f.Consent == nil || !f.Consent.DefinitionChanged {
		return false, nil, ""
	}
	return true, []Fact{
		fact("consent.tool", f.Consent.Tool, "mcp_pin"),
		fact("consent.state", "definition changed since it was approved", "mcp_pin"),
	}, "definition_changed"
}

// Unobserved channels require review at every posture. Direct-IP reuse lasts only for the chat.
func unobservedChannel(f Facts) (bool, []Fact, string) {
	if f.ProcessAccess != "" || f.Containment.HostExecution || f.Containment.ProcessControl {
		covered := f.LeasedExact || f.ExecutionCapabilityLeased
		if !covered {
			subject := "process_control"
			if f.ProcessAccess != "" {
				subject = "native_process:" + f.ProcessAccess
			}
			if f.Containment.HostExecution {
				subject = "host_execution"
			}
			return true, []Fact{fact("boundary.execution", subject, "host")}, subject
		}
		if f.Containment.HostExecution || f.ProcessAccess != "" {
			return false, nil, ""
		}
	}

	// A boundary that did not apply is not leasable: direct IP and socket sets
	// are grantable capabilities, "no boundary ran" is a machine fact.
	if f.Containment.SpawnsProcess && !f.Containment.FSJailed {
		return true, []Fact{
			fact("boundary.applied", "no", "confine"),
			fact("boundary.visibility", "neither writes nor destinations are observed", "confine"),
		}, "unconfined"
	}
	if f.Leased {
		return false, nil, ""
	}
	switch {
	case f.Containment.DirectIP:
		return true, []Fact{
			fact("boundary.egress", EgressDirectIP, "confine"),
			fact("boundary.visibility", "destinations are not observed", "confine"),
		}, "direct_ip"
	case f.Containment.SocketCount > 0:
		// Quieting is scoped to the reviewed socket set.
		return true, []Fact{
			fact("boundary.sockets", strconv.Itoa(f.Containment.SocketCount), "confine"),
			fact("boundary.visibility", "effects inside the service are not observed", "confine"),
		}, "socket:" + f.Containment.SocketPathsDigest
	}
	return false, nil, ""
}

// authorityMisuse reviews declared external or local effects. External effects require network reachability.
// Severity controls posture eligibility; untagged rules count as external.
func authorityMisuse(f Facts) (bool, []Fact, string) {
	if f.Detection == nil || f.LeasedExact {
		return false, nil, ""
	}
	reaches := f.Detection.External && f.Containment.Egress != EgressDeny
	if !reaches && !f.Detection.Local {
		return false, nil, ""
	}
	location := "local_machine"
	if reaches {
		location = "external_system"
	}
	recovery := "unknown"
	if f.Detection.Unrecoverable {
		recovery = "unrecoverable"
	}
	return true, []Fact{
		fact("detection.rule", firstNonEmpty(f.Detection.RuleTitle, f.Detection.RuleID), "detection_pack"),
		fact("detection.pack", f.Detection.PackID, "detection_pack"),
		fact("effect.class", detectionEffectClass(*f.Detection, reaches), "detection_pack"),
		fact("effect.location", location, "detection_pack"),
		fact("effect.recovery", recovery, "detection_pack"),
	}, f.Detection.PackID + "/" + f.Detection.RuleID
}

// detectionEffectClass reports the boundary a match can reach.
func detectionEffectClass(m Match, reaches bool) string {
	effect := "changes something on this machine"
	if reaches {
		effect = "reaches an external system"
	}
	if m.Unrecoverable {
		effect += " and cannot be undone"
	}
	return effect
}

// sensitiveLocation includes protected subjects inside attached roots. Only
// granted-path geometry silences it; a host, tool, or MCP lease does not.
func sensitiveLocation(f Facts) (bool, []Fact, string) {
	if f.File == nil || f.FileLeased {
		return false, nil, ""
	}
	if !f.File.ProtectedSubject && !(f.File.OutsideRoots && f.File.Sensitive) {
		return false, nil, ""
	}
	return true, append(fileFacts(*f.File),
		fact("location.catalog", firstNonEmpty(f.File.CatalogTitle, f.File.CatalogID, "protected path"), "sensitive_locations"),
	), f.File.CatalogID
}

func outsideRootsWrite(f Facts) (bool, []Fact, string) {
	if f.File == nil || f.FileLeased || !f.File.OutsideRoots || f.File.WithinConfinement {
		return false, nil, ""
	}
	if f.File.Mode == ModeRead {
		return false, nil, ""
	}
	return true, fileFacts(*f.File), string(f.File.Mode) + ":" + filepath.Dir(f.File.Path)
}

func outsideRootsRead(f Facts) (bool, []Fact, string) {
	if f.File == nil || f.FileLeased || !f.File.OutsideRoots || f.File.WithinConfinement {
		return false, nil, ""
	}
	if f.File.Mode != ModeRead {
		return false, nil, ""
	}
	return true, fileFacts(*f.File), string(f.File.Mode) + ":" + filepath.Dir(f.File.Path)
}

func fileFacts(t FileTarget) []Fact {
	facts := []Fact{
		fact("file.path", t.Path, "boundary"),
		fact("file.mode", string(t.Mode), "boundary"),
		fact("file.state", "named by the tool, nothing has happened yet", "tool_args"),
	}
	if t.OtherTargetCount > 0 {
		facts = append(facts, fact("file.other_count", strconv.Itoa(t.OtherTargetCount), "tool_args"))
	}
	return facts
}

// agentChosenOutbound reviews first-use sends or tunnels to unconfigured destinations. Plain fetches are ingestion.
func agentChosenOutbound(f Facts, p Posture) (bool, []Fact, string) {
	if f.Destination == nil || f.Leased || f.RequestConsented || f.Destination.Configured {
		return false, nil, ""
	}
	if !f.Destination.Opaque || !f.Destination.FirstUseThisSession {
		return false, nil, ""
	}
	if p.QuietsPublicRegistry(f) {
		return false, nil, ""
	}
	const reason = "opens an opaque tunnel to a destination you did not configure"
	cited := []Fact{
		fact("destination.host", endpointLabel(*f.Destination), "egress_broker"),
		fact("destination.origin", "chosen by the agent, not configured", "egress_broker"),
		fact("effect.class", reason, "egress_broker"),
	}
	if registry := f.Destination.PublicRegistry; registry != "" {
		cited = append(cited, fact("destination.registry", "public package registry: "+registry, "package_registries"))
	}
	if f.Ran.Has(ProducerExposure) && f.SecretExposed && f.Destination.PublicRegistry != "" {
		cited = append(cited, fact("session.state", "this chat has read credential-class content", "exposure_ledger"))
	}
	// Cite ingestion only when its producer reported.
	if f.Ran.Has(ProducerIngestion) && f.UntrustedIngested {
		cited = append(cited, fact("session.state",
			"this chat has read content this app did not author", "evidence_ledger"))
	}
	return true, cited, f.Destination.Host
}

// secretExposedOutbound reviews sends after credential-class content enters the chat.
func secretExposedOutbound(f Facts) (bool, []Fact, string) {
	if f.Destination == nil || f.Leased || f.RequestConsented || !f.SecretExposed {
		return false, nil, ""
	}
	if !f.Destination.Opaque {
		// A readable request the payload lens already screened carries nothing new.
		return false, nil, ""
	}
	return true, []Fact{
		fact("destination.host", endpointLabel(*f.Destination), "egress_broker"),
		fact("session.state", "this chat has read credential-class content", "exposure_ledger"),
		fact("effect.class", "sends content out while a credential is in context", "egress_broker"),
	}, f.Destination.Host
}

func firstHost(f Facts) (bool, []Fact, string) {
	if f.Destination == nil || f.Leased || f.RequestConsented || !f.Destination.FirstUseThisSession {
		return false, nil, ""
	}
	return true, []Fact{
		fact("destination.host", endpointLabel(*f.Destination), "egress_broker"),
		fact("destination.state", "first connection to this host in this chat", "egress_broker"),
	}, f.Destination.Host
}

func mcpUnleased(f Facts) (bool, []Fact, string) {
	if f.MCP == nil || f.Leased {
		return false, nil, ""
	}
	return true, []Fact{
		fact("mcp.tool", f.MCP.Tool, "registry"),
	}, f.MCP.Tool
}

// userRule fires on an ask the person authored. The host made no risk judgement
// here, so the card says whose rule it was and what it matched.
func userRule(f Facts) (bool, []Fact, string) {
	if f.UserRule == nil || f.Leased {
		return false, nil, ""
	}
	cited := []Fact{
		fact("rule.pattern", f.UserRule.Pattern, "approval_rules"),
		fact("rule.category", f.UserRule.Category, "approval_rules"),
	}
	if f.UserRule.Subject != "" {
		cited = append(cited, fact("rule.matched", f.UserRule.Subject, "approval_rules"))
	}
	return true, cited, f.UserRule.Category + ":" + f.UserRule.Pattern
}

func endpointLabel(e Endpoint) string {
	host := strings.TrimSpace(e.Host)
	if host == "" {
		return ""
	}
	if e.Opaque && e.Port > 0 {
		return host + ":" + strconv.Itoa(int(e.Port))
	}
	return host
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}

// UnsealedDecision requests review when gate evaluation is missing.
func UnsealedDecision() *Decision {
	return &Decision{
		Primary: api.GateIncompleteFacts,
		Cited: []Fact{
			fact("gate", "approval gate not yet constructed", "boot"),
		},
	}
}
