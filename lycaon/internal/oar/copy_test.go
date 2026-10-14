package oar

import (
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/oarcore"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestParseCopyCollectsBindings(t *testing.T) {
	refs, err := oarcore.ParseCopy("Use {{ tool }}{% if arg_validation_reason %} — {{ arg_validation_reason }}{% endif %}")
	testutil.FailErr(t, "parse copy", err)
	got := map[string]bool{}
	for _, b := range refs {
		got[b.Name] = true
	}
	if !got["tool"] || !got["arg_validation_reason"] {
		t.Fatalf("refs=%v", refs)
	}
}

func TestParseCopyRejectsFor(t *testing.T) {
	_, err := oarcore.ParseCopy("{% for p in paths %}{{ p }}{% endfor %}")
	if err == nil || !strings.Contains(err.Error(), "for") {
		t.Fatalf("err=%v, want token for", err)
	}
}

func TestRenderCopyJoinsListsAndOmitsZero(t *testing.T) {
	got := oarcore.RenderCopy(
		"{{ tool }}: {{ paintedwolf.capability_request_fields }}{% if arg_validation_reason %} — {{ arg_validation_reason }}{% endif %}",
		map[string]any{
			"tool":                                  "command",
			"paintedwolf.capability_request_fields": []string{"host_resources", "direct_ip"},
			"arg_validation_reason":                 "",
		},
	)
	if got != "command: host_resources, direct_ip" {
		t.Fatalf("render = %q", got)
	}
}

func TestValidateRuleCopyUnknownName(t *testing.T) {
	err := ValidateRuleCopy(&Rule{
		ID:   "GHOST",
		Copy: Copy{What: "hello {{ ghost_field }}"},
	})
	if err == nil || !strings.Contains(err.Error(), "[OAR-COPY-2]") {
		t.Fatalf("err=%v", err)
	}
}

func TestCopyDoesNotChangeDecisionCode(t *testing.T) {
	r := &Rule{ID: "X", Effect: EffectBlock, Copy: Copy{What: "blocked {{ tool }}"}}
	gc := NewGuardContext()
	gc.Invocation.Tool = "read"
	a := decisionFromRule(r, gc, map[string]any{"tool": "read"})
	r.Copy.What = "different {{ tool }}"
	b := decisionFromRule(r, gc, map[string]any{"tool": "read"})
	if a.Code != b.Code || a.Effect != b.Effect {
		t.Fatalf("copy changed the decision: %#v vs %#v", a, b)
	}
	if a.Copy["what"] == b.Copy["what"] {
		t.Fatal("rendered copy should differ when the member differs")
	}
}

func TestOccurrenceFactDeclsPublishedInActivation(t *testing.T) {
	tiers := factTiers()
	for _, d := range occurrenceFactDecls {
		if got := tiers[d.name]; got != d.tier {
			t.Fatalf("factTiers()[%q] = %q, want %q", d.name, got, d.tier)
		}
	}
	gc := NewGuardContext()
	gc.Invocation.Tool = "command"
	gc.Session.WorkerLeg = "leg-1"
	act := activation(gc)
	fields, ok := act["paintedwolf.capability_request_fields"].([]string)
	if !ok || len(fields) == 0 {
		t.Fatalf("paintedwolf.capability_request_fields = %#v, want non-empty []string", act["paintedwolf.capability_request_fields"])
	}
	if act["paintedwolf.supports_local_listen"] != true || act["paintedwolf.supports_loopback_connect"] != true {
		t.Fatalf("listen/connect occurrence facts = (%v, %v), want (true, true)", act["paintedwolf.supports_local_listen"], act["paintedwolf.supports_loopback_connect"])
	}
	if act["paintedwolf.worker_leg"] != "leg-1" {
		t.Fatalf("paintedwolf.worker_leg = %v, want leg-1", act["paintedwolf.worker_leg"])
	}
}

