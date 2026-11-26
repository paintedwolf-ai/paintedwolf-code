package pongoplain

import (
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
)

func recordRenders(t *testing.T, rec *BranchRecorder, src string, renders ...map[string]any) {
	t.Helper()
	tpl, err := Compile(src)
	testutil.FailErr(t, "compile", err)
	ActiveBranchRecorder.Store(rec)
	defer ActiveBranchRecorder.Store(nil)
	for _, data := range renders {
		if _, err := ExecuteDetached(tpl, data); err != nil {
			t.Fatalf("render %v: %v", data, err)
		}
	}
}

func TestBranchRecorderCountsEveryArmAtItsInspectedSite(t *testing.T) {
	src := "{% if a %}A{% elif b %}B{% else %}C{% endif %}\n{% for x in xs %}{{ x }}{% empty %}E{% endfor %}"
	rec := &BranchRecorder{Aliases: map[string]string{InlineTemplateID(src): "greeting.md"}}
	recordRenders(t, rec, src,
		map[string]any{"a": true, "xs": []int{1}},
		map[string]any{"b": true},
		map[string]any{},
	)

	analysis, err := Inspect(src, Isolated)
	testutil.FailErr(t, "inspect", err)
	want := map[BranchKind][2]int{
		BranchIf:    {1, 2},
		BranchElif:  {1, 1},
		BranchElse:  {1, 2},
		BranchFor:   {1, 2},
		BranchEmpty: {2, 1},
	}
	if len(rec.Hits) != len(want) || len(analysis.Branches) != len(want) {
		t.Fatalf("hits = %d, inspected branches = %d, want %d", len(rec.Hits), len(analysis.Branches), len(want))
	}
	for _, spec := range analysis.Branches {
		hit := rec.Hits[branchKey("greeting.md", spec.Line, spec.Col, spec.Kind)]
		if hit == nil {
			t.Fatalf("no hit at the inspected %s site %d:%d; hits %v", spec.Kind, spec.Line, spec.Col, rec.Hits)
		}
		if hit.Template != "greeting.md" || hit.RawTemplate != InlineTemplateID(src) {
			t.Errorf("%s hit attributed to %q (raw %q)", spec.Kind, hit.Template, hit.RawTemplate)
		}
		if got := [2]int{hit.TakenTrue, hit.TakenFalse}; got != want[spec.Kind] {
			t.Errorf("%s taken true/false = %v, want %v", spec.Kind, got, want[spec.Kind])
		}
	}
}

func conditionHits(rec *BranchRecorder) map[BranchKind]*ConditionHit {
	out := map[BranchKind]*ConditionHit{}
	for _, hit := range rec.Conditions {
		out[hit.Kind] = hit
	}
	return out
}

func TestObservedConditionsMarkAtomsTheEvaluatorCannotRead(t *testing.T) {
	// A false left operand short-circuits the filter that would fail.
	src := `{% if a and d|date:"2006" %}x{% elif f(a) %}y{% endif %}`
	rec := &BranchRecorder{ObserveConditions: true, Aliases: map[string]string{InlineTemplateID(src): "guard.md"}}
	recordRenders(t, rec, src, map[string]any{"a": false, "d": "not a time"})

	hits := conditionHits(rec)
	short := hits[BranchIf]
	if short == nil || short.Template != "guard.md" {
		t.Fatalf("if condition hit = %+v", short)
	}
	if want := []string{"a", `d|date:"2006"`}; !slices.Equal(short.Atoms, want) {
		t.Fatalf("if atoms = %q, want %q", short.Atoms, want)
	}
	if len(short.Vectors) != 1 {
		t.Fatalf("if vectors = %d, want 1", len(short.Vectors))
	}
	if v := short.Vectors[0]; !slices.Equal(v.Atoms, []Truth{False, Unknown}) || v.Decision || v.Recomposes {
		t.Fatalf("short-circuited vector = %+v, want [False Unknown] deciding false without recomposition", *v)
	}

	// A call is outside the expression grammar, so its atoms are not observed.
	unparsed := hits[BranchElif]
	if unparsed == nil || len(unparsed.Atoms) != 0 || len(unparsed.Vectors) != 1 {
		t.Fatalf("unparsable elif hit = %+v", unparsed)
	}
	if v := unparsed.Vectors[0]; len(v.Atoms) != 0 || v.Decision || v.Recomposes {
		t.Fatalf("unparsable vector = %+v, want no atoms and no recomposition", *v)
	}
}

func TestInspectSkipsOpaqueRegionsAndToleratesTagMarkers(t *testing.T) {
	type branch struct {
		kind BranchKind
		expr string
	}
	for _, tc := range []struct {
		src  string
		want []branch
	}{
		{`{# {% if a %} #}{% if b %}{% endif %}`, []branch{{BranchIf, "b"}}},
		{`{% comment %}{% if a %}{% endcomment %}{% if b %}{% endif %}`, []branch{{BranchIf, "b"}}},
		{`{% verbatim %}{% for x in y %}{% endverbatim %}`, nil},
		{`{# never closed {% if a %}`, nil},
		{`{% %}x{% if a %}{% endif %}`, []branch{{BranchIf, "a"}}},
		{`{%- if a -%}x{%- endif -%}`, []branch{{BranchIf, "a"}}},
		{`{% - if a %}{% endif %}`, []branch{{BranchIf, "a"}}},
		{`{%- - %}`, nil},
		{`{% if a %}{% elif b %}{% else %}{% endif %}`, []branch{{BranchIf, "a"}, {BranchElif, "b"}, {BranchElse, ""}}},
		{`{% for x in xs %}a{% empty %}b{% endfor %}`, []branch{{BranchFor, "x in xs"}, {BranchEmpty, ""}}},
		{`{% if a %}{% unclosed`, []branch{{BranchIf, "a"}}},
	} {
		analysis, err := Inspect(tc.src, Isolated)
		if err != nil {
			t.Errorf("Inspect(%q): %v", tc.src, err)
			continue
		}
		var got []branch
		for _, b := range analysis.Branches {
			got = append(got, branch{b.Kind, b.Expression})
		}
		if !slices.Equal(got, tc.want) {
			t.Errorf("Inspect(%q) branches = %v, want %v", tc.src, got, tc.want)
		}
	}
}

func TestInspectDependenciesAndRefusals(t *testing.T) {
	composed, err := Inspect(`{% include "a.md" %}{% include 'b.md' with x=1 %}`, Composed)
	testutil.FailErr(t, "inspect composed", err)
	if want := []string{"a.md", "b.md"}; !slices.Equal(composed.Dependencies, want) {
		t.Fatalf("dependencies = %q, want %q", composed.Dependencies, want)
	}
	isolated, err := Inspect(`{% include "a.md" %}`, Isolated)
	testutil.FailErr(t, "inspect isolated", err)
	if len(isolated.Dependencies) != 0 {
		t.Fatalf("isolated dependencies = %q, want none", isolated.Dependencies)
	}

	for name, tc := range map[string]struct {
		src     string
		profile Profile
		want    error
	}{
		"wall clock":    {`{% now "2006" %}`, Isolated, ErrNondeterministic},
		"too many tags": {strings.Repeat(`{% x %}`, MaxTemplateTags+1), Isolated, ErrSourceLimit},
		"loop nesting":  {`{% for a in x %}{% for b in x %}{% for c in x %}`, Isolated, ErrExecutionLimit},
	} {
		if _, err := Inspect(tc.src, tc.profile); !errors.Is(err, tc.want) {
			t.Errorf("%s: err = %v, want %v", name, err, tc.want)
		}
	}
	if _, err := Inspect(`x`, Profile(0)); err == nil {
		t.Fatal("Inspect accepted an unknown profile")
	}
}

// An unobserved render produces the observed render's output and records no
// branch, condition or render.
func TestUnobservedRenderRecordsNothing(t *testing.T) {
	src := "{% if a and b %}A{% else %}C{% endif %}{% for x in xs %}{{ x }}{% empty %}E{% endfor %}"
	tpl, err := Compile(src)
	testutil.FailErr(t, "compile", err)
	data := map[string]any{"a": true, "b": false, "xs": []int{1, 2}}
	want, err := ExecuteDetached(tpl, data)
	testutil.FailErr(t, "plain render", err)
	rec := &BranchRecorder{ObserveConditions: true}
	ActiveBranchRecorder.Store(rec)
	defer ActiveBranchRecorder.Store(nil)
	got, err := ExecuteUnobserved(t.Context(), tpl, data)
	testutil.FailErr(t, "unobserved render", err)
	if got != want {
		t.Fatalf("unobserved render = %q, want %q", got, want)
	}
	if len(rec.Hits) != 0 || len(rec.Conditions) != 0 || len(rec.Rendered) != 0 {
		t.Fatalf("unobserved render recorded hits=%d conditions=%d rendered=%v", len(rec.Hits), len(rec.Conditions), rec.Rendered)
	}
	if _, err := Execute(t.Context(), tpl, data); err != nil {
		t.Fatalf("observed render: %v", err)
	}
	if len(rec.Hits) == 0 || len(rec.Conditions) == 0 || len(rec.Rendered) != 1 {
		t.Fatalf("observed render recorded hits=%d conditions=%d rendered=%v", len(rec.Hits), len(rec.Conditions), rec.Rendered)
	}
}
