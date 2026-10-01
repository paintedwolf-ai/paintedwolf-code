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
	gc.Tool = "read"
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
