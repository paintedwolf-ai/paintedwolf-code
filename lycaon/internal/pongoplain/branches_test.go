package pongoplain

import (
	"fmt"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
)

func TestBranchRecorderTracksIfElifElseForAndEmptyOutcomes(t *testing.T) {
	source := `{# {% if ignored_comment %}x{% endif %} #}` + "\n" +
		`{% comment %}{% if ignored_block %}y{% endif %}{% endcomment %}` + "\n" +
		`{% verbatim %}{% for x in ignored %}z{% endfor %}{% endverbatim %}` + "\n" +
		`{%- if mode == "alpha" -%}` + "\n" +
		`alpha` + "\n" +
		`{% elif mode == "beta" %}` + "\n" +
		`beta` + "\n" +
		`{% else %}` + "\n" +
		`other` + "\n" +
		`{% endif %}` + "\n" +
		`{% for item in items %}[{{ item }}]{% empty %}[none]{% endfor %}`

	analysis, err := Inspect(source, Isolated)
	testutil.FailErr(t, "Inspect", err)
	if len(analysis.Branches) != 5 {
		t.Fatalf("discovered %d branches, want 5: %+v", len(analysis.Branches), analysis.Branches)
	}

	wantKinds := []BranchKind{BranchIf, BranchElif, BranchElse, BranchFor, BranchEmpty}
	for i, want := range wantKinds {
		if analysis.Branches[i].Kind != want {
			t.Fatalf("branch[%d].Kind = %q, want %q", i, analysis.Branches[i].Kind, want)
		}
	}

	tpl, err := Compile(source)
	testutil.FailErr(t, "Compile", err)

	rec := &BranchRecorder{
		Aliases: map[string]string{InlineTemplateID(source): "unit/sample.md"},
	}
	ActiveBranchRecorder.Store(rec)
	t.Cleanup(func() { ActiveBranchRecorder.Store(nil) })

	for _, vars := range []map[string]any{
		{"mode": "alpha", "items": []string{"a", "b"}},
		{"mode": "beta", "items": []string{}},
		{"mode": "gamma", "items": []string{"only"}},
	} {
		_, execErr := Execute(t.Context(), tpl, vars)
		testutil.FailErr(t, "Execute", execErr)
	}

	for _, spec := range analysis.Branches {
		id := fmt.Sprintf("unit/sample.md@L%d:C%d:%s", spec.Line, spec.Col, spec.Kind)
		hit, ok := rec.Hits[id]
		if !ok {
			t.Fatalf("missing runtime hit for discovered branch %s (hits=%+v)", id, rec.Hits)
		}
		if hit.TakenTrue == 0 || hit.TakenFalse == 0 {
			t.Fatalf("branch %s vacuous: TakenTrue=%d TakenFalse=%d", id, hit.TakenTrue, hit.TakenFalse)
		}
	}
}
