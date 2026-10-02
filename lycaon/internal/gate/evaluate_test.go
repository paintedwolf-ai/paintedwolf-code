package gate

import (
	"slices"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/pkg/api"
)

// fixture is one gate's firing case and its silent counterpart.
type fixture struct {
	gate    api.ApprovalGate
	posture Posture
	// fires is a Facts value the gate must fire on.
	fires Facts
	// silent is a Facts value that differs from fires in exactly the conjunct
	// under test, and must not fire.
	silent Facts
}

func allProducers() Producer {
	return ProducerContainment | ProducerDetection | ProducerPayload |
		ProducerDestination | ProducerFilePath | ProducerConsent | ProducerLease |
		ProducerRule | ProducerExposure | ProducerIngestion | ProducerPackageExecution
}

func baseFacts(stage Stage) Facts {
	return Facts{
		Stage:       stage,
		Ran:         allProducers(),
		Containment: Containment{SpawnsProcess: true, FSJailed: true, Egress: EgressProxy},
	}
}

func fixtures() []fixture {
	policyFires := baseFacts(StagePreSpawn)
	policyFires.AgentPolicyPaths = []string{"/project/AGENTS.md"}
	policyLeased := policyFires
	policyLeased.AgentPolicyLeased = true
	secretFires := baseFacts(StagePreSend)
	secretFires.Payload = &SecretHit{Surface: "command", RuleID: "aws-key", RuleTitle: "AWS key"}
	secretSilent := baseFacts(StagePreSend)
	secretSilent.Payload = &SecretHit{
		Surface: "model_request", RuleID: "aws-key", RuleTitle: "AWS key",
		Source: SecretSourcePublicInbound, SourceKind: "tool_result", SourceTool: "fetch_url",
	}

	driftFires := baseFacts(StagePreSpawn)
	driftFires.Consent = &Consent{Tool: "mcp.acme.deploy", DefinitionChanged: true}
	driftSilent := baseFacts(StagePreSpawn)
	driftSilent.Consent = &Consent{Tool: "mcp.acme.deploy"}

	packageFires := baseFacts(StagePreSpawn)
	age := 2
	packageFires.PackageExecution = &PackageExecution{
		Manager: "npm", Operation: "remote_execute",
		Packages: []PackageIdentity{{System: "NPM", Name: "create-app", Version: "1.2.3", AgeDays: &age, Status: "resolved"}},
	}
	packageSilent := packageFires
	packageSilent.LeasedExact = true

	packageKnownFires := baseFacts(StagePreSpawn)
	knownAge := 30
	packageKnownFires.PackageExecution = &PackageExecution{
		Manager: "npm", Operation: "remote_execute",
		Packages: []PackageIdentity{{System: "NPM", Name: "create-app", Version: "1.2.3", AgeDays: &knownAge, Status: "resolved"}},
	}
	packageKnownSilent := packageKnownFires
	packageKnownSilent.LeasedPackage = true

	unobservedFires := baseFacts(StagePreSpawn)
	unobservedFires.Containment = Containment{SpawnsProcess: true, FSJailed: true, Egress: EgressDirectIP, DirectIP: true}
	unobservedSilent := unobservedFires
	unobservedSilent.Leased = true

	authorityFires := baseFacts(StagePreSpawn)
	authorityFires.Detection = &Match{
		PackID: "publish-release", RuleID: "npm-publish", RuleTitle: "Publish a package",
		Level: "high", External: true, Unrecoverable: true,
	}
	// A recoverable external effect still fires. Only a rule that declares no
	// place its effect lands stays silent.
	authoritySilent := baseFacts(StagePreSpawn)
	authoritySilent.Detection = &Match{
		PackID: "publish-release", RuleID: "npm-publish", RuleTitle: "Publish a package",
		Level: "high", External: false, Local: false, Unrecoverable: true,
	}

	sensitiveFires := baseFacts(StagePreSpawn)
	sensitiveFires.File = &FileTarget{
		Path: "/Users/x/.zshrc", Mode: ModeWrite,
		OutsideRoots: true, Sensitive: true,
		CatalogID: "shell-profile", CatalogTitle: "Shell startup file",
	}
	// Same crossing, unlisted location: silent at Balanced.
	sensitiveSilent := baseFacts(StagePreSpawn)
	sensitiveSilent.File = &FileTarget{
		Path: "/Users/x/.cargo/registry", Mode: ModeWrite,
		OutsideRoots: true,
	}

	outsideWriteFires := baseFacts(StagePreSpawn)
	outsideWriteFires.File = &FileTarget{
		Path: "/mnt/data/notes.md", Mode: ModeWrite, OutsideRoots: true,
	}
	outsideWriteSilent := baseFacts(StagePreSpawn)
	outsideWriteSilent.File = &FileTarget{Path: "/proj/notes.md", Mode: ModeWrite}

	outsideReadFires := baseFacts(StagePreSpawn)
	outsideReadFires.File = &FileTarget{
		Path: "/mnt/data/notes.md", Mode: ModeRead, OutsideRoots: true,
	}
	outsideReadSilent := baseFacts(StagePreSpawn)
	outsideReadSilent.File = &FileTarget{Path: "/proj/notes.md", Mode: ModeRead}

	outboundFires := baseFacts(StagePreDial)
	outboundFires.Destination = &Endpoint{Host: "paste.example", Transport: "http", Opaque: true, FirstUseThisSession: true}
	outboundSilent := baseFacts(StagePreDial)
	outboundSilent.Destination = &Endpoint{
		Host: "registry.npmjs.org", Transport: "http",
		Configured: true, ConfiguredBy: "provider_endpoints", Opaque: true, FirstUseThisSession: true,
	}

	exposedFires := baseFacts(StagePreDial)
	exposedFires.SecretExposed = true
	// A configured destination: the exposure is what changed, not where it goes.
	exposedFires.Destination = &Endpoint{Host: "api.example", Transport: "http", Configured: true, Opaque: true}
	exposedSilent := exposedFires
	exposedSilent.SecretExposed = false

	firstHostFires := baseFacts(StagePreDial)
	firstHostFires.Destination = &Endpoint{Host: "registry.npmjs.org", Configured: true, FirstUseThisSession: true}
	firstHostSilent := baseFacts(StagePreDial)
	firstHostSilent.Destination = &Endpoint{Host: "registry.npmjs.org", Configured: true}

	mcpFires := baseFacts(StagePreSpawn)
	mcpFires.MCP = &MCPCall{Tool: "mcp.acme.deploy"}
	mcpSilent := mcpFires
	mcpSilent.Leased = true

	ruleFires := baseFacts(StagePreSpawn)
	ruleFires.UserRule = &UserRule{Category: "command", Pattern: "git push*", Subject: "git push --tags"}
	ruleSilent := ruleFires
	ruleSilent.Leased = true

	actionSetFires := baseFacts(StagePreSpawn)
	actionSetFires.Ran |= ProducerApprovalRequest
	actionSetFires.ApprovalRequest = &ApprovalRequest{Count: 3}
	actionSetSilent := actionSetFires
	actionSetSilent.ApprovalRequest = &ApprovalRequest{}

	wideningFires := baseFacts(StagePreSpawn)
	wideningFires.Ran |= ProducerApprovalRequest
	wideningFires.CapabilityWidening = &CapabilityWidening{Axes: []string{AxisLocalListen}}
	wideningSilent := wideningFires
	wideningSilent.CapabilityWidening = &CapabilityWidening{}

	return []fixture{
		{gate: api.GateAgentPolicyChange, posture: PostureBalanced, fires: policyFires, silent: policyLeased},
		{gate: api.GateSecretOutbound, posture: PostureLight, fires: secretFires, silent: secretSilent},
		{gate: api.GateConsentDrift, posture: PostureLight, fires: driftFires, silent: driftSilent},
		{gate: api.GateRemotePackageExecution, posture: PostureLight, fires: packageFires, silent: packageSilent},
		{gate: api.GateRemotePackageExecutionKnown, posture: PostureBalanced, fires: packageKnownFires, silent: packageKnownSilent},
		{gate: api.GateUnobservedChannel, posture: PostureLight, fires: unobservedFires, silent: unobservedSilent},
		{gate: api.GateAuthorityMisuse, posture: PostureBalanced, fires: authorityFires, silent: authoritySilent},
		{gate: api.GateSensitiveLocation, posture: PostureBalanced, fires: sensitiveFires, silent: sensitiveSilent},
		{gate: api.GateOutsideRootsWrite, posture: PostureLight, fires: outsideWriteFires, silent: outsideWriteSilent},
		{gate: api.GateOutsideRootsRead, posture: PostureLight, fires: outsideReadFires, silent: outsideReadSilent},
		{gate: api.GateAgentChosenOutbound, posture: PostureBalanced, fires: outboundFires, silent: outboundSilent},
		{gate: api.GateSecretExposedOutbound, posture: PostureStrict, fires: exposedFires, silent: exposedSilent},
		{gate: api.GateFirstHost, posture: PostureStrict, fires: firstHostFires, silent: firstHostSilent},
		{gate: api.GateMCPUnleased, posture: PostureStrict, fires: mcpFires, silent: mcpSilent},
		{gate: api.GateUserRule, posture: PostureLight, fires: ruleFires, silent: ruleSilent},
		{gate: api.GateExplicitApprovalRequest, posture: PostureLight, fires: actionSetFires, silent: actionSetSilent},
		{gate: api.GateCapabilityWidening, posture: PostureStrict, fires: wideningFires, silent: wideningSilent},
	}
}

func TestFixtureTableFiresAndStaysSilent(t *testing.T) {
	for _, f := range fixtures() {
		verdict, decision := Evaluate(f.fires, f.posture)
		if verdict != Ask {
			t.Errorf("%s: fires case returned %s, want ask", f.gate, verdict)
			continue
		}
		if !decisionNames(decision, f.gate) {
			t.Errorf("%s: fires case cited %v", f.gate, decision.Gates())
		}
		if len(decision.Cited) == 0 {
			t.Errorf("%s: fired with no cited facts", f.gate)
		}
		switch f.gate {
		case api.GateExplicitApprovalRequest, api.GateCapabilityWidening, api.GateIncompleteFacts,
			api.GateAgentChosenOutbound, api.GateSecretExposedOutbound, api.GateFirstHost, api.GateMCPUnleased:
			// These gates carry no subject in ReasonKey (empty discriminator).
			if strings.Contains(decision.ReasonKey, "/") || strings.Contains(decision.ReasonKey, "@") {
				t.Errorf("%s: ReasonKey %q looks subject-shaped", f.gate, decision.ReasonKey)
			}
		default:
			if decision.ReasonKey == "" {
				t.Errorf("%s: fired with no reason key", f.gate)
			}
		}

		verdict, decision = Evaluate(f.silent, f.posture)
		if verdict == Ask && decisionNames(decision, f.gate) {
			t.Errorf("%s: silent case fired anyway (cited %v)", f.gate, decision.Cited)
		}
	}
}

// Trust is the only conjunct that differs between these two facts.
func TestSecretOutboundStaysSilentForATrustedDestination(t *testing.T) {
	facts := baseFacts(StagePreSend)
	facts.Payload = &SecretHit{
		Surface: "model_request", RuleID: "aws-key", RuleTitle: "AWS key",
		Source: SecretSourceUnknown, SourceKind: "tool_result", SourceTool: "read",
	}
	if verdict, _ := Evaluate(facts, PostureLight); verdict != Ask {
		t.Fatalf("untrusted destination verdict = %s, want ask", verdict)
	}
	facts.Payload.DestinationTrusted = true
	verdict, decision := Evaluate(facts, PostureStrict)
	if verdict != Silent {
		t.Fatalf("trusted destination verdict = %s (cited %v), want silent at every posture", verdict, decision.Cited)
	}
}

// A chat's generated secret reaching local recipients is the one subject
// posture releases; every other combination asks at every posture.
func TestSecretOutboundReleasesChatSecretsLocallyBelowStrict(t *testing.T) {
	cases := []struct {
		name                   string
		generated, local, asks bool
		posture                Posture
	}{
		{"light releases", true, true, false, PostureLight},
		{"balanced releases", true, true, false, PostureBalanced},
		{"strict asks", true, true, true, PostureStrict},
		{"remote recipient asks", true, false, true, PostureLight},
		{"person's secret asks", false, true, true, PostureLight},
		{"unparsed posture asks as strict", true, true, true, Posture("unset")},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			facts := baseFacts(StagePreSend)
			facts.Payload = &SecretHit{
				Surface: "command", RuleID: "managed-secret", RuleTitle: "A managed secret",
				ChatGenerated: tt.generated, RecipientsLocal: tt.local,
			}
			verdict, decision := Evaluate(facts, tt.posture)
			if (verdict == Ask) != tt.asks {
				t.Fatalf("verdict = %s, want asks %v", verdict, tt.asks)
			}
			if tt.asks && decision.Primary != api.GateSecretOutbound {
				t.Fatalf("primary = %s, want secret_outbound", decision.Primary)
			}
			if tt.asks && tt.generated && !slices.ContainsFunc(decision.Cited, func(f Fact) bool { return f.Key == "secret.origin" }) {
				t.Fatalf("a card for a generated secret does not say so: %v", decision.Cited)
			}
		})
	}
}

// A value a person stored asks at every posture, even toward a trusted
// destination, a public-inbound source, or the chat's own processes.
func TestHeldSecretAlwaysAsks(t *testing.T) {
	for _, posture := range []Posture{PostureLight, PostureBalanced, PostureStrict} {
		for name, hit := range map[string]SecretHit{
			"local chat value":     {ChatGenerated: true, RecipientsLocal: true},
			"trusted destination":  {DestinationTrusted: true},
			"public inbound value": {Source: SecretSourcePublicInbound},
		} {
			t.Run(string(posture)+"/"+name, func(t *testing.T) {
				hit.Surface, hit.RuleID, hit.RuleTitle, hit.Held = "command", "managed-secret", "A managed secret", true
				facts := baseFacts(StagePreSend)
				facts.Payload = &hit
				verdict, decision := Evaluate(facts, posture)
				if verdict != Ask || !slices.ContainsFunc(decision.Cited, func(f Fact) bool { return f.Key == "secret.custody" }) {
					t.Fatalf("held value verdict = %s cited = %v", verdict, decision)
				}
			})
		}
	}
}

func TestExplicitApprovalRequestNeedsOnlyItsTypedProducer(t *testing.T) {
	verdict, decision := Evaluate(Facts{
		Stage: StagePreSpawn, Ran: ProducerApprovalRequest,
		ApprovalRequest: &ApprovalRequest{Count: 2},
	}, DefaultPosture)
	if verdict != Ask || decision == nil || decision.Primary != api.GateExplicitApprovalRequest || len(decision.Also) != 0 {
		t.Fatalf("decision = %+v, verdict = %s", decision, verdict)
	}
}

func TestCapabilityWideningNeedsOnlyItsTypedProducer(t *testing.T) {
	verdict, decision := Evaluate(Facts{
		Stage: StagePreSpawn, Ran: ProducerApprovalRequest,
		CapabilityWidening: &CapabilityWidening{Axes: []string{AxisLocalListen, AxisLoopbackConnect}},
	}, DefaultPosture)
	if verdict != Ask || decision == nil || decision.Primary != api.GateCapabilityWidening || len(decision.Also) != 0 {
		t.Fatalf("decision = %+v, verdict = %s", decision, verdict)
	}
}

func TestCapabilityWideningPrefersOverExplicitRequest(t *testing.T) {
	verdict, decision := Evaluate(Facts{
		Stage: StagePreSpawn, Ran: ProducerApprovalRequest,
		ApprovalRequest:    &ApprovalRequest{Count: 1},
		CapabilityWidening: &CapabilityWidening{Axes: []string{AxisLocalListen}},
	}, PostureStrict)
	if verdict != Ask || decision == nil || decision.Primary != api.GateCapabilityWidening {
		t.Fatalf("decision = %+v, verdict = %s", decision, verdict)
	}
	for _, g := range decision.Gates() {
		if g == api.GateExplicitApprovalRequest {
			t.Fatal("capability widening must not also cite explicit_approval_request")
		}
	}
}

func TestCapabilityWideningPostureNuances(t *testing.T) {
	// Light: silent for any local network capability widening.
	verdict, _ := Evaluate(Facts{
		Stage: StagePreSpawn, Ran: ProducerApprovalRequest,
		CapabilityWidening: &CapabilityWidening{Axes: []string{AxisLocalListen, AxisLoopbackConnect}},
	}, PostureLight)
	if verdict != Silent {
		t.Fatalf("light posture returned %s, want silent", verdict)
	}

	// Balanced: silent for unprivileged loopback listen.
	verdict, _ = Evaluate(Facts{
		Stage: StagePreSpawn, Ran: ProducerApprovalRequest,
		CapabilityWidening: &CapabilityWidening{Axes: []string{AxisLocalListen}, Ports: []uint16{8123}},
	}, PostureBalanced)
	if verdict != Silent {
		t.Fatalf("balanced posture unprivileged listen returned %s, want silent", verdict)
	}

	// Balanced: silent for self-spawned loopback connect.
	verdict, _ = Evaluate(Facts{
		Stage: StagePreSpawn, Ran: ProducerApprovalRequest,
		CapabilityWidening: &CapabilityWidening{Axes: []string{AxisLoopbackConnect}, Ports: []uint16{8123}, SelfSpawned: true},
	}, PostureBalanced)
	if verdict != Silent {
		t.Fatalf("balanced posture self-spawned loopback connect returned %s, want silent", verdict)
	}

	// Balanced: asks for foreign loopback connect (SSRF protection).
	verdict, decision := Evaluate(Facts{
		Stage: StagePreSpawn, Ran: ProducerApprovalRequest,
		CapabilityWidening: &CapabilityWidening{Axes: []string{AxisLoopbackConnect}, Ports: []uint16{6379}, SelfSpawned: false},
	}, PostureBalanced)
	if verdict != Ask || decision == nil || decision.Primary != api.GateCapabilityWidening {
		t.Fatalf("balanced posture foreign loopback connect returned %s (%+v), want ask", verdict, decision)
	}

	// Balanced: asks for privileged listen port.
	verdict, decision = Evaluate(Facts{
		Stage: StagePreSpawn, Ran: ProducerApprovalRequest,
		CapabilityWidening: &CapabilityWidening{Axes: []string{AxisLocalListen}, Ports: []uint16{80}},
	}, PostureBalanced)
	if verdict != Ask || decision == nil || decision.Primary != api.GateCapabilityWidening {
		t.Fatalf("balanced posture privileged listen returned %s (%+v), want ask", verdict, decision)
	}

	// Strict: asks for unprivileged loopback listen.
	verdict, decision = Evaluate(Facts{
		Stage: StagePreSpawn, Ran: ProducerApprovalRequest,
		CapabilityWidening: &CapabilityWidening{Axes: []string{AxisLocalListen}, Ports: []uint16{8123}},
	}, PostureStrict)
	if verdict != Ask || decision == nil || decision.Primary != api.GateCapabilityWidening {
		t.Fatalf("strict posture unprivileged listen returned %s (%+v), want ask", verdict, decision)
	}
}

// Every predicate gate has a fixture; structural gates are covered by the fail-closed test.
func TestEveryGateHasAFixtureAndADefinition(t *testing.T) {
	defined := map[api.ApprovalGate]bool{}
	for _, d := range definitions() {
		defined[d.gate] = true
		if len(d.stages) == 0 {
			t.Errorf("%s: no stage declared", d.gate)
		}
		if d.requires == 0 {
			t.Errorf("%s: requires no producer, so a missing fact would read as safe", d.gate)
		}
	}
	covered := map[api.ApprovalGate]bool{}
	for _, f := range fixtures() {
		covered[f.gate] = true
	}
	for _, g := range All() {
		if g == api.GateIncompleteFacts {
			if defined[g] {
				t.Errorf("%s must stay structural, not a predicate", g)
			}
			continue
		}
		if !defined[g] {
			t.Errorf("%s has no definition", g)
		}
		if !covered[g] {
			t.Errorf("%s has no fixture", g)
		}
		if rank(g) >= len(All()) {
			t.Errorf("%s has no place in the citation order", g)
		}
	}
}

// Every gate is reachable from some posture or is structural.
func TestEveryGateIsReachableFromAPosture(t *testing.T) {
	for _, g := range All() {
		if g == api.GateIncompleteFacts {
			for _, p := range []Posture{PostureLight, PostureBalanced, PostureStrict} {
				if p.Enables(g) {
					t.Errorf("%s must not be posture-selected", g)
				}
			}
			continue
		}
		reachable := false
		for _, p := range []Posture{PostureLight, PostureBalanced, PostureStrict} {
			if p.Enables(g) {
				reachable = true
			}
		}
		if !reachable {
			t.Errorf("%s is enabled by no posture", g)
		}
	}
}

func TestPostureLaddersAreMonotonic(t *testing.T) {
	for _, g := range PostureLight.Gates() {
		if !PostureBalanced.Enables(g) {
			t.Errorf("balanced drops %s that light asks", g)
		}
	}
	for _, g := range PostureBalanced.Gates() {
		if !PostureStrict.Enables(g) {
			t.Errorf("strict drops %s that balanced asks", g)
		}
	}
}

func TestOutsideRootsLiveAtEveryPosture(t *testing.T) {
	if !PostureBalanced.Enables(api.GateOutsideRootsWrite) {
		t.Fatal("balanced must enable outside_roots_write so native outside writes can raise a grant card")
	}
	if !PostureBalanced.Enables(api.GateOutsideRootsRead) {
		t.Fatal("balanced must enable outside_roots_read so native outside reads can raise a grant card")
	}
	if !PostureLight.Enables(api.GateOutsideRootsWrite) {
		t.Fatal("light must enable outside_roots_write")
	}
	if !PostureLight.Enables(api.GateOutsideRootsRead) {
		t.Fatal("light must enable outside_roots_read so native outside reads can raise a grant card")
	}
	if !PostureStrict.Enables(api.GateOutsideRootsWrite) || !PostureStrict.Enables(api.GateOutsideRootsRead) {
		t.Fatal("strict must keep outside_roots")
	}
}

// Missing producer facts require approval.
func TestMissingProducerFailsClosed(t *testing.T) {
	for _, f := range fixtures() {
		facts := f.fires
		facts.Ran = 0
		verdict, decision := Evaluate(facts, f.posture)
		if verdict != Ask {
			t.Fatalf("%s: no producers returned %s, want a fail-closed ask", f.gate, verdict)
		}
		if decision.Primary != api.GateIncompleteFacts {
			t.Fatalf("%s: no producers cited %s, want %s", f.gate, decision.Primary, api.GateIncompleteFacts)
		}
		if len(decision.Cited) == 0 {
			t.Fatalf("%s: fail-closed ask cited nothing", f.gate)
		}
	}
}

// Silence requires complete producer coverage.
func TestSilenceRequiresProducersToHaveRun(t *testing.T) {
	facts := baseFacts(StagePreDial)
	facts.Destination = &Endpoint{Host: "registry.npmjs.org", Configured: true}
	if verdict, _ := Evaluate(facts, PostureBalanced); verdict != Silent {
		t.Fatalf("configured destination with every producer = %s, want silent", verdict)
	}
	facts.Ran &^= ProducerDestination
	if verdict, decision := Evaluate(facts, PostureBalanced); verdict != Ask || decision.Primary != api.GateIncompleteFacts {
		t.Fatalf("dropping the destination producer = %s, want a fail-closed ask", verdict)
	}
}

// A reuse-none primary withholds the ladder for the whole card: a lease that
// cleared the card would clear the withheld reason with it.
func TestNarrowestGateClampsTheWholeLadder(t *testing.T) {
	facts := baseFacts(StagePreSpawn)
	facts.Containment = Containment{SpawnsProcess: true, FSJailed: true, Egress: EgressDirectIP, DirectIP: true}
	facts.Consent = &Consent{Tool: "mcp.acme.deploy", DefinitionChanged: true}

	verdict, decision := Evaluate(facts, PostureBalanced)
	if verdict != Ask {
		t.Fatalf("verdict = %s", verdict)
	}
	if decision.Primary != api.GateConsentDrift {
		t.Fatalf("primary = %s, want %s to outrank the channel gate", decision.Primary, api.GateConsentDrift)
	}
	// Consent drift keeps reuse on the exact action across every duration.
	reuse := decision.Reuse()
	if reuse.Shape != ReuseExactAction {
		t.Fatalf("card carrying %v composed shape %q, want exact_action", decision.Gates(), reuse.Shape)
	}
	if !reuse.Scope.Durable() {
		t.Fatalf("card carrying %v lost its durable rung: %+v", decision.Gates(), reuse)
	}
	seenUnobserved := false
	for _, cited := range decision.Cited {
		if cited.Gate == api.GateUnobservedChannel {
			seenUnobserved = true
		}
		if !seenUnobserved && cited.Gate != api.GateConsentDrift {
			t.Fatalf("citation %q attributed to %q appears before primary-gate evidence", cited.Key, cited.Gate)
		}
	}
	if !seenUnobserved {
		t.Fatalf("secondary gate has no attributed citation: %+v", decision.Cited)
	}
}

// Scope composes to the narrowest ceiling. No gate caps below project, so the
// narrowing shows between project and device.
func TestNarrowestScopeWins(t *testing.T) {
	narrow := &Decision{Primary: api.GateUserRule, Also: []api.ApprovalGate{api.GateAgentChosenOutbound}}
	if got := narrow.Reuse().Scope; got != ScopeProject {
		t.Fatalf("scope = %q, want %q — the device gate must not widen its project-capped sibling", got, ScopeProject)
	}
	wide := &Decision{Primary: api.GateUserRule, Also: []api.ApprovalGate{api.GateOutsideRootsWrite}}
	if got := wide.Reuse().Scope; got != ScopeDevice {
		t.Fatalf("scope = %q, want %q", got, ScopeDevice)
	}
}

func TestReasonKeyIgnoresSubject(t *testing.T) {
	a := baseFacts(StagePreSpawn)
	a.File = &FileTarget{
		Path: "/Users/x/.aws/credentials", Mode: ModeRead,
		OutsideRoots: true, Sensitive: true, CatalogID: "aws-credentials",
	}
	b := a
	b.File = &FileTarget{
		Path: "/Users/x/.ssh/id_rsa", Mode: ModeRead,
		OutsideRoots: true, Sensitive: true, CatalogID: "aws-credentials",
	}
	_, da := Evaluate(a, PostureBalanced)
	_, db := Evaluate(b, PostureBalanced)
	if da == nil || db == nil || da.Primary != api.GateSensitiveLocation || db.Primary != api.GateSensitiveLocation {
		t.Fatalf("want sensitive_location primary, got %v / %v", da, db)
	}
	if sensitiveReasonSegment(da.ReasonKey) == "" ||
		sensitiveReasonSegment(da.ReasonKey) != sensitiveReasonSegment(db.ReasonKey) {
		t.Fatalf("same catalog reason keys differ: %q vs %q", da.ReasonKey, db.ReasonKey)
	}
	c := a
	c.File = &FileTarget{
		Path: "/Users/x/.aws/credentials", Mode: ModeRead,
		OutsideRoots: true, Sensitive: true, CatalogID: "ssh-key",
	}
	_, dc := Evaluate(c, PostureBalanced)
	if dc == nil || sensitiveReasonSegment(dc.ReasonKey) == sensitiveReasonSegment(da.ReasonKey) {
		t.Fatalf("different catalog ids share ReasonKey %q", da.ReasonKey)
	}
}

// sensitiveReasonSegment returns the sensitive_location:… piece of a composed
// ReasonKey so path-keyed outside_roots Also segments do not fork catalog identity.
func sensitiveReasonSegment(reasonKey string) string {
	for _, part := range strings.Split(reasonKey, "|") {
		if strings.HasPrefix(part, string(api.GateSensitiveLocation)+":") {
			return part
		}
	}
	return ""
}

func TestReuseShapeAndScopeAgreeOnNone(t *testing.T) {
	for _, g := range All() {
		r := ReuseFor(g)
		if (r.Shape == ReuseNone) != (r.Scope == ScopeNone) {
			t.Fatalf("%s: shape %q and scope %q disagree on none", g, r.Shape, r.Scope)
		}
		if r.Offered() != (r.Shape != ReuseNone && r.Scope != ScopeNone) {
			t.Fatalf("%s: Offered=%v for %+v", g, r.Offered(), r)
		}
	}
}

func TestNarrowestShapeWins(t *testing.T) {
	d := &Decision{Primary: api.GateAuthorityMisuse, Also: []api.ApprovalGate{api.GateSensitiveLocation}}
	got := d.Reuse()
	if got.Shape != ReuseExactAction || got.Scope != ScopeProject {
		t.Fatalf("reuse = %+v, want exact_action/project (narrowest of detection+filesystem)", got)
	}
}

// Detection leases cover exact actions, even when a broader host lease exists.
func TestDetectionLeaseSilencesOnlyTheExactAction(t *testing.T) {
	facts := baseFacts(StagePreSpawn)
	facts.Containment = Containment{SpawnsProcess: true, FSJailed: true, Egress: EgressProxy}
	facts.Detection = &Match{PackID: "p", RuleID: "r", External: true, Unrecoverable: true}
	facts.Leased = true
	if verdict, decision := Evaluate(facts, PostureBalanced); verdict != Ask || decision.Primary != api.GateAuthorityMisuse {
		t.Fatalf("family lease silenced detection: %s/%v", verdict, decision)
	}
	facts.LeasedExact = true
	if verdict, decision := Evaluate(facts, PostureBalanced); verdict != Silent {
		t.Fatalf("exact lease must silence detection, got %s/%v", verdict, decision)
	}
}

// Filesystem gates require path grants, even when the action has a lease.
func TestFilesystemGatesReadOnlyGrantedPathCoverage(t *testing.T) {
	for _, tc := range []struct {
		name   string
		target *FileTarget
		want   api.ApprovalGate
	}{
		{
			name: "protected subject inside the roots",
			target: &FileTarget{
				Path: "/proj/.env", Mode: ModeWrite, ProtectedSubject: true,
				CatalogID: "dotenv", CatalogTitle: "Environment file",
			},
			want: api.GateSensitiveLocation,
		},
		{
			name:   "ordinary path outside the roots",
			target: &FileTarget{Path: "/mnt/data/notes.md", Mode: ModeWrite, OutsideRoots: true},
			want:   api.GateOutsideRootsWrite,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			facts := baseFacts(StagePreSpawn)
			facts.File = tc.target
			facts.Leased = true
			verdict, decision := Evaluate(facts, PostureBalanced)
			if verdict != Ask || decision.Primary != tc.want {
				t.Fatalf("action lease silenced %s: %s/%v", tc.want, verdict, decision)
			}
			facts.FileLeased = true
			if verdict, decision := Evaluate(facts, PostureBalanced); verdict != Silent {
				t.Fatalf("granted-path coverage must silence %s, got %s/%v", tc.want, verdict, decision)
			}
		})
	}
}

// Untagged detections require approval.
func TestUntaggedDetectionIsTreatedAsGateShaped(t *testing.T) {
	facts := baseFacts(StagePreSpawn)
	facts.Detection = &Match{PackID: "p", RuleID: "r", External: true, Unrecoverable: true}
	if verdict, _ := Evaluate(facts, PostureBalanced); verdict != Ask {
		t.Fatalf("verdict = %s, want ask", verdict)
	}
}

// Contained detections are recorded without an approval ask.
func TestContainedDetectionDoesNotAsk(t *testing.T) {
	facts := baseFacts(StagePreSpawn)
	facts.Containment = Containment{SpawnsProcess: true, FSJailed: true, Egress: EgressDeny}
	facts.Detection = &Match{PackID: "p", RuleID: "r", External: true, Unrecoverable: true}
	if verdict, decision := Evaluate(facts, PostureBalanced); verdict != Silent {
		t.Fatalf("verdict = %s (%v), want silent", verdict, decision)
	}
}

// Balanced posture permits ordinary fetches from configured hosts.
func TestOrdinaryConfiguredFetchIsSilent(t *testing.T) {
	facts := baseFacts(StagePreDial)
	facts.Destination = &Endpoint{
		Host: "proxy.golang.org", Transport: "connect", Port: 443,
		Configured: true, Opaque: true, FirstUseThisSession: true,
	}
	if verdict, decision := Evaluate(facts, PostureBalanced); verdict != Silent {
		t.Fatalf("verdict = %s (%v), want silent", verdict, decision.Gates())
	}
	if verdict, _ := Evaluate(facts, PostureStrict); verdict != Ask {
		t.Fatalf("strict verdict = %s, want ask on first host", verdict)
	}
}

func decisionNames(d *Decision, g api.ApprovalGate) bool {
	for _, candidate := range d.Gates() {
		if candidate == g {
			return true
		}
	}
	return false
}

// A native host tool spawns no process, so an empty containment describes it
// rather than reporting an absent boundary.
func TestNativeToolWithoutAProcessBoundaryIsSilent(t *testing.T) {
	facts := Facts{Stage: StagePreSpawn, Ran: allProducers()}
	if verdict, decision := Evaluate(facts, PostureBalanced); verdict != Silent {
		t.Fatalf("verdict = %s (%v), want silent", verdict, decision.Gates())
	}
	facts.Containment.SpawnsProcess = true
	if verdict, decision := Evaluate(facts, PostureBalanced); verdict != Ask ||
		decision.Primary != api.GateUnobservedChannel {
		t.Fatalf("an unconfined process must ask, got %s/%v", verdict, decision)
	}
}

// Every posture asks on a read or write outside the attached roots; a listed
// location adds sensitive_location from Balanced up.
func TestFilesystemPostureSplit(t *testing.T) {
	unlistedRead := baseFacts(StagePreSpawn)
	unlistedRead.File = &FileTarget{
		Path: "/Users/x/.cargo/registry", Mode: ModeRead,
		OutsideRoots: true,
	}
	unlistedWrite := baseFacts(StagePreSpawn)
	unlistedWrite.File = &FileTarget{
		Path: "/Users/x/.cargo/registry", Mode: ModeWrite,
		OutsideRoots: true,
	}
	listedRead := baseFacts(StagePreSpawn)
	listedRead.File = &FileTarget{
		Path: "/Users/x/Documents/tax.pdf", Mode: ModeRead,
		OutsideRoots: true, Sensitive: true, CatalogID: "user-documents",
	}

	if verdict, d := Evaluate(unlistedRead, PostureLight); verdict != Ask || d.Primary != api.GateOutsideRootsRead {
		t.Errorf("light must ask on an unlisted read crossing, got %s/%v", verdict, d)
	}
	if verdict, d := Evaluate(unlistedWrite, PostureLight); verdict != Ask || d.Primary != api.GateOutsideRootsWrite {
		t.Errorf("light must ask on an unlisted write crossing, got %s/%v", verdict, d)
	}
	if verdict, d := Evaluate(unlistedRead, PostureBalanced); verdict != Ask || d.Primary != api.GateOutsideRootsRead {
		t.Errorf("balanced must ask on an unlisted read crossing, got %s/%v", verdict, d)
	}
	if verdict, d := Evaluate(unlistedWrite, PostureBalanced); verdict != Ask || d.Primary != api.GateOutsideRootsWrite {
		t.Errorf("balanced must ask on an unlisted write crossing, got %s/%v", verdict, d)
	}
	if verdict, d := Evaluate(unlistedRead, PostureStrict); verdict != Ask || d.Primary != api.GateOutsideRootsRead {
		t.Errorf("strict must ask on unlisted read crossing, got %s/%v", verdict, d)
	}
	if verdict, d := Evaluate(unlistedWrite, PostureStrict); verdict != Ask || d.Primary != api.GateOutsideRootsWrite {
		t.Errorf("strict must ask on unlisted write crossing, got %s/%v", verdict, d)
	}
	// sensitive_location and outside_roots_read both fire and collapse into one card.
	for _, posture := range []Posture{PostureBalanced, PostureStrict} {
		verdict, d := Evaluate(listedRead, posture)
		if verdict != Ask || d.Primary != api.GateSensitiveLocation || len(d.Also) != 1 || d.Also[0] != api.GateOutsideRootsRead {
			t.Fatalf("%s must collapse both filesystem gates into one card, got %v", posture, d.Gates())
		}
	}
}

// Filesystem approvals remain time-bounded.
func TestFilesystemGatesAreLeasable(t *testing.T) {
	for _, g := range []api.ApprovalGate{api.GateSensitiveLocation, api.GateOutsideRootsWrite, api.GateOutsideRootsRead} {
		if r := ReuseFor(g); r.Shape != ReusePredicate || r.Scope != ScopeDevice {
			t.Errorf("%s reuse = %+v, want predicate/device", g, r)
		}
	}
}

func TestProtectedSubjectInsideAttachedRootStillAsks(t *testing.T) {
	inside := baseFacts(StagePreSpawn)
	inside.File = &FileTarget{
		Path: "/Users/x/repo/.netrc", Mode: ModeWrite,
		OutsideRoots: false, ProtectedSubject: true,
	}
	for _, p := range []Posture{PostureBalanced, PostureStrict} {
		verdict, d := Evaluate(inside, p)
		if verdict != Ask || d == nil || d.Primary != api.GateSensitiveLocation {
			t.Errorf("%s: an in-root protected subject must ask, got %v %v", p, verdict, d.Gates())
		}
	}
	if verdict, _ := Evaluate(inside, PostureLight); verdict != Silent {
		t.Error("Light disables the sensitive-location gate; the posture opt-down must hold")
	}
}

// Attached roots suppress catalog sensitivity for their descendants.
func TestAttachedRootInsideASensitiveLocationDoesNotAsk(t *testing.T) {
	inside := baseFacts(StagePreSpawn)
	inside.File = &FileTarget{
		Path: "/Users/x/Documents/my-project/src/main.go", Mode: ModeRead,
		OutsideRoots: false, Sensitive: true,
		CatalogID: "user-documents",
	}
	for _, p := range []Posture{PostureLight, PostureBalanced, PostureStrict} {
		if verdict, d := Evaluate(inside, p); verdict != Silent {
			t.Errorf("%s: work inside an attached root must be silent, got %v", p, d.Gates())
		}
	}

	// Sibling and parent paths remain outside scope.
	for _, path := range []string{
		"/Users/x/Documents/other-project/secrets.txt",
		"/Users/x/Documents",
	} {
		outside := baseFacts(StagePreSpawn)
		outside.File = &FileTarget{
			Path: path, Mode: ModeRead,
			OutsideRoots: true, Sensitive: true, CatalogID: "user-documents",
		}
		if verdict, d := Evaluate(outside, PostureBalanced); verdict != Ask ||
			d.Primary != api.GateSensitiveLocation {
			t.Errorf("%s must still ask, got %s/%v", path, verdict, d)
		}
	}
}

// Exposure gating is Strict-only; payload screening applies at every posture.
func TestSecretExposureIsStrictOnly(t *testing.T) {
	f := baseFacts(StagePreDial)
	f.SecretExposed = true
	f.Destination = &Endpoint{Host: "api.example", Transport: "http", Configured: true, Opaque: true}
	for _, p := range []Posture{PostureLight, PostureBalanced} {
		if verdict, _ := Evaluate(f, p); verdict == Ask {
			t.Errorf("%s must not ask on secret exposure alone", p)
		}
	}
}

// A fetch that only retrieves carries nothing out, so exposure alone does not
// make reading a page an interruption.
func TestExposureDoesNotGateAPlainFetch(t *testing.T) {
	f := baseFacts(StagePreDial)
	f.SecretExposed = true
	f.Destination = &Endpoint{Host: "docs.example", Transport: "http", Configured: true}
	if verdict, decision := Evaluate(f, PostureStrict); verdict == Ask && decisionNames(decision, api.GateSecretExposedOutbound) {
		t.Fatal("a body-less fetch must not fire the exposure gate")
	}
}

// A destination the user's own configuration names is not agent-chosen, however
// unfamiliar it looks, so Balanced stays quiet for it.
func TestConfiguredDestinationIsSilentAtBalanced(t *testing.T) {
	f := baseFacts(StagePreDial)
	f.Destination = &Endpoint{
		Host: "git.internal", Port: 443, Transport: "connect",
		Configured: true, ConfiguredBy: "provider_endpoints",
		Opaque: true, FirstUseThisSession: true,
	}
	if verdict, decision := Evaluate(f, PostureBalanced); verdict == Ask {
		t.Fatalf("a configured destination must not ask at Balanced, got %v", decision.Gates())
	}
	// Strict still confirms the first reach.
	if verdict, _ := Evaluate(f, PostureStrict); verdict != Ask {
		t.Fatal("Strict must still confirm a first host")
	}
}

// Second reach to the same host is silent even at Strict: first_host means first.
func TestSecondReachIsSilentAtStrict(t *testing.T) {
	f := baseFacts(StagePreDial)
	f.Destination = &Endpoint{
		Host: "git.internal", Port: 443, Transport: "connect",
		Configured: true, ConfiguredBy: "provider_endpoints", Opaque: true,
	}
	if verdict, decision := Evaluate(f, PostureStrict); verdict == Ask {
		t.Fatalf("a host already reached must not ask again, got %v", decision.Gates())
	}
}

// A public package registry is baseline at Balanced and below, and stops being
// baseline once credential-class content is in the chat.
func TestPublicRegistryIsBaselineAtBalancedAndBelow(t *testing.T) {
	registry := func(exposed bool) Facts {
		f := baseFacts(StagePreDial)
		f.UntrustedIngested = true
		f.SecretExposed = exposed
		f.Destination = &Endpoint{
			Host: "index.crates.io", Port: 443, Transport: "connect",
			PublicRegistry: "crates.io", Opaque: true, FirstUseThisSession: true,
		}
		return f
	}
	for _, p := range []Posture{PostureLight, PostureBalanced} {
		if verdict, decision := Evaluate(registry(false), p); verdict == Ask {
			t.Errorf("%s must not ask for a public registry, got %v", p, decision.Gates())
		}
	}
	verdict, decision := Evaluate(registry(true), PostureBalanced)
	if verdict != Ask || !decisionNames(decision, api.GateAgentChosenOutbound) {
		t.Fatalf("credential exposure must withdraw the registry baseline, got %s/%v", verdict, decision)
	}
	if verdict, _ := Evaluate(registry(false), PostureStrict); verdict != Ask {
		t.Fatal("Strict must still confirm first contact with a registry")
	}

	unknown := registry(false)
	unknown.Ran &^= ProducerExposure
	if verdict, _ := Evaluate(unknown, PostureBalanced); verdict != Ask {
		t.Fatal("an unknown exposure state must not grant the registry baseline")
	}
}

// Local-effect detections can require approval when egress is denied.
func TestAuthorityMisuseLocalEffect(t *testing.T) {
	t.Parallel()
	match := func(external, local bool) *Match {
		return &Match{
			PackID: "command-destructive", RuleID: "recursive-delete",
			RuleTitle: "Recursively delete a directory tree", Level: "high",
			External: external, Local: local, Unrecoverable: true,
		}
	}
	cases := []struct {
		name      string
		detection *Match
		egress    string
		wantAsk   bool
		wantClass string
	}{
		{"local fires under egress deny", match(false, true), EgressDeny, true, "changes something on this machine and cannot be undone"},
		{"local fires under proxy", match(false, true), EgressProxy, true, "changes something on this machine and cannot be undone"},
		{"external needs a way out", match(true, false), EgressDeny, false, ""},
		{"external fires under proxy", match(true, false), EgressProxy, true, "reaches an external system and cannot be undone"},
		{"no declared class stays silent", match(false, false), EgressProxy, false, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			ok, facts, _ := authorityMisuse(Facts{
				Detection:   tc.detection,
				Containment: Containment{Egress: tc.egress},
			})
			if ok != tc.wantAsk {
				t.Fatalf("ask = %v, want %v", ok, tc.wantAsk)
			}
			if !ok {
				return
			}
			var got string
			for _, f := range facts {
				if f.Key == "effect.class" {
					got = f.Value
				}
			}
			if got != tc.wantClass {
				t.Fatalf("effect.class = %q, want %q", got, tc.wantClass)
			}
		})
	}
}
