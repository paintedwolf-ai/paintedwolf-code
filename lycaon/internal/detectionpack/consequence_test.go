package detectionpack

import (
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/hitl"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestNewEventProjectsConsequenceFacts(t *testing.T) {
	t.Parallel()
	ev := NewEvent(ActionObservation{
		Tool:                  "mcp__stripe__charge",
		ProjectDir:            "/proj",
		SessionID:             "s1",
		ActionID:              "a1",
		APIActions:            []string{"stripe:charges.create", "stripe:charges.create"},
		ActionEffects:         []string{"charge", "CHARGE", "bogus"},
		TargetScopes:          []string{"item"},
		PrincipalScopes:       []string{"named"},
		CredentialPersistence: []string{"session"},
		BulkAction:            "false",
		AmountPresent:         "true",
		TargetPresent:         "unknown",
		Boundary:              hitl.Contained{FSJailed: true, Egress: hitl.ContainedEgressDirectIP, Roots: []string{"/proj"}, DirectIP: true, LoopbackAccess: true},
	})
	if got := strings.Join(ev.ApiAction, ","); got != "stripe:charges.create" {
		t.Fatalf("ApiAction=%q", got)
	}
	if got := strings.Join(ev.ActionEffect, ","); got != "charge" {
		t.Fatalf("ActionEffect=%q (bogus must drop)", got)
	}
	if ev.BulkAction != "false" || ev.AmountPresent != "true" || ev.TargetPresent != "unknown" {
		t.Fatalf("presence=%+v", ev)
	}
	if ev.LoopbackAccess != "true" {
		t.Fatalf("LoopbackAccess=%q", ev.LoopbackAccess)
	}
	for _, field := range []string{
		"ActionEffect", "TargetScope", "PrincipalScope", "CredentialPersistence",
		"BulkAction", "AmountPresent", "TargetPresent", "LoopbackAccess",
	} {
		if _, ok := ev.lookup(field); !ok {
			t.Fatalf("lookup missing %s", field)
		}
	}
}

func TestUnknownConsequenceNeverSatisfiesStrongSelection(t *testing.T) {
	t.Parallel()
	r := mustRule(t, `title: Strong
id: 11111111-1111-4111-8111-111111111111
description: d
logsource: {product: lycaon, service: tool_exec}
level: high
detection:
  sel:
    ActionEffect: purge
    TargetScope: account
    PrincipalScope: public
    CredentialPersistence: long_lived
    BulkAction: "true"
    AmountPresent: "true"
  condition: sel`)
	ev := NewEvent(ActionObservation{
		Tool:                  "command",
		CommandLine:           "noop",
		ActionEffects:         []string{"unknown"},
		TargetScopes:          []string{"unknown"},
		PrincipalScopes:       []string{"unknown"},
		CredentialPersistence: []string{"unknown"},
		BulkAction:            "unknown",
		AmountPresent:         "unknown",
		TargetPresent:         "unknown",
		Boundary:              hitl.Contained{FSJailed: true, Egress: "proxy"},
	})
	if r.Matches(ev) {
		t.Fatal("unknown facts must not satisfy strong selections")
	}
	empty := NewEvent(ActionObservation{
		Tool:     "command",
		Boundary: hitl.Contained{FSJailed: true, Egress: "proxy"},
	})
	if r.Matches(empty) {
		t.Fatal("absent facts must not satisfy strong selections")
	}
}

func TestTypedSelectionRejectsUnsupportedValues(t *testing.T) {
	t.Parallel()
	r, err := ParseRule([]byte(`title: Bad
id: 22222222-2222-4222-8222-222222222222
description: d
logsource: {product: lycaon, service: tool_exec}
level: high
detection:
  sel:
    ActionEffect: explode
  condition: sel
`))
	testutil.FailErr(t, "ParseRule", err)
	if r.Supported || !strings.Contains(r.UnsupportedReason, "unsupported value") {
		t.Fatalf("expected inactive rule, got %+v", r)
	}
}

func TestConsequenceRuleMatchesMappedFacts(t *testing.T) {
	t.Parallel()
	r := mustRule(t, `title: Charge
id: 33333333-3333-4333-8333-333333333333
description: d
logsource: {product: lycaon, service: tool_exec}
level: critical
detection:
  sel:
    ActionEffect: charge
    AmountPresent: "true"
    TargetPresent: "true"
  condition: sel`)
	ev := NewEvent(ActionObservation{
		Tool:          "mcp__payments__charge",
		ActionEffects: []string{"charge"},
		AmountPresent: "true",
		TargetPresent: "true",
		Boundary:      hitl.Contained{FSJailed: true, Egress: "proxy"},
	})
	if !r.Matches(ev) {
		t.Fatal("mapped charge facts must match")
	}
}
