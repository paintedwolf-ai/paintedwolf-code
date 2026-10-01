package tools

import (
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/gate"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestEveryGateHasExplainCopy(t *testing.T) {
	for _, g := range gate.All() {
		decision := &gate.Decision{
			Primary: g,
			Cited:   []gate.Fact{{Key: "detection.rule", Value: "a rule", Source: "test"}},
		}
		out := ExplainGate(ApprovalExplanation{}, decision)
		if strings.TrimSpace(out.What) == "" {
			t.Errorf("%s: no explain copy — the card would show no reason", g)
		}
	}
}

func TestExplainGateOutsideRootsUsesReadFraming(t *testing.T) {
	out := ExplainGate(ApprovalExplanation{
		What:      "Change files outside attached folders at /tmp/notes.md.",
		AllowLine: "changing files outside attached folders",
	}, &gate.Decision{
		Primary: api.GateOutsideRootsRead,
		Cited: []gate.Fact{
			{Key: "file.mode", Value: "read", Source: "boundary"},
			{Key: "file.path", Value: "/tmp/notes.md", Source: "boundary"},
		},
	})
	if !strings.Contains(out.What, "Read files") {
		t.Fatalf("read outside_roots What = %q", out.What)
	}
	if strings.Contains(out.What, "Change files") {
		t.Fatalf("read outside_roots must not say Change: %q", out.What)
	}
	if out.AllowLine != "reading files outside attached folders" {
		t.Fatalf("AllowLine = %q", out.AllowLine)
	}
}

func TestExplainGateOutsideRootsWrite(t *testing.T) {
	out := ExplainGate(ApprovalExplanation{}, &gate.Decision{
		Primary: api.GateOutsideRootsWrite,
		Cited: []gate.Fact{
			{Key: "file.mode", Value: "write", Source: "boundary"},
			{Key: "file.path", Value: "/tmp/notes.md", Source: "boundary"},
		},
	})
	if !strings.Contains(strings.ToLower(out.What), "this writes outside the folders you attached") {
		t.Fatalf("write outside_roots What = %q", out.What)
	}
}

func TestExplainGateRemotePackageExecutionCountsTheSet(t *testing.T) {
	decisionFor := func(coordinates ...string) *gate.Decision {
		cited := []gate.Fact{{Key: "package.manager", Value: "pip", Source: "test"}}
		for _, c := range coordinates {
			cited = append(cited, gate.Fact{Key: "package.coordinate", Value: c, Source: "test"})
		}
		return &gate.Decision{Primary: api.GateRemotePackageExecution, Cited: cited}
	}

	one := ExplainGate(ApprovalExplanation{}, decisionFor("pip@26.2.1"))
	if !strings.Contains(one.What, "the package version shown above") {
		t.Fatalf("single package What = %q", one.What)
	}

	many := ExplainGate(ApprovalExplanation{}, decisionFor(
		"pip@26.2.1", "pytest@9.1.1", "python-dotenv@1.2.3", "requests@2.34.2",
	))
	if !strings.Contains(many.What, "the 4 package versions shown above") {
		t.Fatalf("package set What = %q", many.What)
	}
	if strings.Contains(many.IfWrong, "approving a different version requires") {
		t.Fatalf("package set IfWrong still reads as one package: %q", many.IfWrong)
	}
}

func TestExplainDetectionNamesWhereTheEffectLands(t *testing.T) {
	decision := func(location string) *gate.Decision {
		return &gate.Decision{
			Primary: api.GateAuthorityMisuse,
			Cited: []gate.Fact{
				{Key: "detection.rule", Value: "Recursively delete a directory tree", Source: "detection_pack"},
				{Key: "effect.location", Value: location, Source: "detection_pack"},
				{Key: "effect.recovery", Value: "unrecoverable", Source: "detection_pack"},
			},
		}
	}
	local := ExplainGate(ApprovalExplanation{}, decision("local_machine"))
	if strings.Contains(local.IfWrong, "external") || !strings.Contains(local.IfWrong, "on this machine") {
		t.Fatalf("local detection copy = %q", local.IfWrong)
	}
	external := ExplainGate(ApprovalExplanation{}, decision("external_system"))
	if !strings.Contains(external.IfWrong, "external system") {
		t.Fatalf("external detection copy = %q", external.IfWrong)
	}
}

func TestExplainGateWithoutADecisionIsUnchanged(t *testing.T) {
	base := ApprovalExplanation{What: "run a command"}
	if got := ExplainGate(base, nil); got != base {
		t.Fatalf("copy changed without a decision: %+v", got)
	}
}
