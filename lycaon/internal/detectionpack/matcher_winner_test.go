package detectionpack

import (
	"testing"

	"github.com/lycaon/lycaon/internal/gate"
	"github.com/lycaon/lycaon/internal/hitl"
)

func TestMatcherWinnerTiesAndOverlaps(t *testing.T) {
	t.Parallel()
	highRule := mustRule(t, `title: High
id: aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa
description: d
logsource: {product: lycaon, service: tool_exec}
level: high
detection:
  sel: {CommandLine|contains: ' boom '}
  condition: sel`)
	mediumRule := mustRule(t, `title: Med
id: bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb
description: d
logsource: {product: lycaon, service: tool_exec}
level: medium
detection:
  sel: {CommandLine|contains: ' boom '}
  condition: sel`)
	earlyHigh := mustRule(t, `title: Early
id: cccccccc-cccc-4ccc-8ccc-cccccccccccc
description: d
logsource: {product: lycaon, service: tool_exec}
level: high
detection:
  sel: {CommandLine|contains: ' boom '}
  condition: sel`)
	lateHigh := mustRule(t, `title: Late
id: dddddddd-dddd-4ddd-8ddd-dddddddddddd
description: d
logsource: {product: lycaon, service: tool_exec}
level: high
detection:
  sel: {CommandLine|contains: ' boom '}
  condition: sel`)
	egressOnly := mustRule(t, `title: Egress
id: eeeeeeee-eeee-4eee-8eee-eeeeeeeeeeee
description: d
logsource: {product: lycaon, service: egress_observed}
level: critical
detection:
  sel: {DestinationHostname: evil.example}
  condition: sel`)
	inert := mustRule(t, `title: Inert
id: ffffffff-ffff-4fff-8fff-ffffffffffff
description: d
logsource: {product: lycaon, service: tool_exec}
level: informational
detection:
  sel: {CommandLine|contains: ' boom '}
  condition: sel`)
	unsupported := mustRule(t, `title: Ok
id: 12121212-1212-4121-8121-121212121212
description: d
logsource: {product: lycaon, service: tool_exec}
level: critical
detection:
  sel: {CommandLine|contains: ' boom '}
  condition: sel`)
	unsupported.Supported = false
	unsupported.UnsupportedReason = "forced"

	t.Run("high beats medium", func(t *testing.T) {
		t.Parallel()
		m := NewMatcher(&Catalog{Packs: []Pack{
			{ID: "p-med", Enabled: true, Rules: []Rule{mediumRule}},
			{ID: "p-high", Enabled: true, Rules: []Rule{highRule}},
		}})
		hit, ok := m.Match(testEvent("command", "do boom now", "/p", true, "proxy", "s"))
		if !ok || hit.Level != LevelHigh || hit.PackID != "p-high" {
			t.Fatalf("hit=%+v ok=%v", hit, ok)
		}
	})
	t.Run("equal severity lower pack id wins", func(t *testing.T) {
		t.Parallel()
		m := NewMatcher(&Catalog{Packs: []Pack{
			{ID: "zzz", Enabled: true, Rules: []Rule{highRule}},
			{ID: "aaa", Enabled: true, Rules: []Rule{mustRule(t, `title: Other
id: 34343434-3434-4343-8343-343434343434
description: d
logsource: {product: lycaon, service: tool_exec}
level: high
detection:
  sel: {CommandLine|contains: ' boom '}
  condition: sel`)}},
		}})
		hit, ok := m.Match(testEvent("command", "do boom now", "/p", true, "proxy", "s"))
		if !ok || hit.PackID != "aaa" {
			t.Fatalf("hit=%+v ok=%v", hit, ok)
		}
	})
	t.Run("equal pack severity earlier file order wins", func(t *testing.T) {
		t.Parallel()
		m := NewMatcher(&Catalog{Packs: []Pack{{
			ID: "same", Enabled: true, Rules: []Rule{earlyHigh, lateHigh},
		}}})
		hit, ok := m.Match(testEvent("command", "do boom now", "/p", true, "proxy", "s"))
		if !ok || hit.RuleID != earlyHigh.ID {
			t.Fatalf("hit=%+v ok=%v", hit, ok)
		}
	})
	t.Run("unrelated source ignored", func(t *testing.T) {
		t.Parallel()
		m := NewMatcher(&Catalog{Packs: []Pack{{
			ID: "mix", Enabled: true, Rules: []Rule{egressOnly, mediumRule},
		}}})
		hit, ok := m.Match(testEvent("command", "do boom now", "/p", true, "proxy", "s"))
		if !ok || hit.Level != LevelMedium {
			t.Fatalf("hit=%+v ok=%v", hit, ok)
		}
	})
	t.Run("disabled unsupported inert ignored", func(t *testing.T) {
		t.Parallel()
		m := NewMatcher(&Catalog{Packs: []Pack{
			{ID: "off", Enabled: false, Rules: []Rule{highRule}},
			{ID: "bad", Enabled: true, Rules: []Rule{unsupported, inert, mediumRule}},
		}})
		hit, ok := m.Match(testEvent("command", "do boom now", "/p", true, "proxy", "s"))
		if !ok || hit.Level != LevelMedium || hit.PackID != "bad" {
			t.Fatalf("hit=%+v ok=%v", hit, ok)
		}
	})
}

func TestEscalatesPostureBands(t *testing.T) {
	t.Parallel()
	cases := []struct {
		level   Level
		posture gate.Posture
		want    bool
	}{
		{LevelCritical, "light", true},
		{LevelCritical, "balanced", true},
		{LevelCritical, "strict", true},
		{LevelHigh, "light", false},
		{LevelHigh, "balanced", true},
		{LevelHigh, "strict", true},
		{LevelMedium, "light", false},
		{LevelMedium, "balanced", false},
		{LevelMedium, "strict", true},
		{LevelLow, "strict", false},
		{LevelInformational, "strict", false},
	}
	for _, tc := range cases {
		if got := Escalates(tc.level, tc.posture); got != tc.want {
			t.Fatalf("%s under %s = %v want %v", tc.level, tc.posture, got, tc.want)
		}
	}
}

func TestEvaluateToolExecNamedSeam(t *testing.T) {
	t.Parallel()
	r := mustRule(t, `title: Named
id: 56565656-5656-4565-8565-565656565656
description: d
logsource: {product: lycaon, service: tool_exec}
level: high
detection:
  sel: {Image: curl}
  condition: sel`)
	m := NewMatcher(&Catalog{Packs: []Pack{{ID: "n", Enabled: true, Rules: []Rule{r}}}})
	eval := EvaluateToolExec(m, ActionObservation{
		Tool: "command", CommandLine: "curl https://x", ProjectDir: "/p", SessionID: "s",
		Boundary: hitl.Contained{FSJailed: true, Egress: hitl.ContainedEgressProxy},
	})
	if !eval.OK || eval.Match.RuleID != r.ID {
		t.Fatalf("eval=%+v", eval)
	}
	if EvaluateToolExec(nil, ActionObservation{}).OK {
		t.Fatal("nil matcher must not match")
	}
}
