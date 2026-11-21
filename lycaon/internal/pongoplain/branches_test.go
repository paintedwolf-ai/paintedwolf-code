package pongoplain

import (
	"fmt"
	"io"
	"strings"
	"sync"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
)

type memTemplateLoader map[string]string

func (memTemplateLoader) Abs(_, name string) string { return name }
func (m memTemplateLoader) Get(path string) (io.Reader, error) {
	src, ok := m[path]
	if !ok {
		return nil, fmt.Errorf("missing template %q", path)
	}
	return strings.NewReader(src), nil
}

func TestExecuteNormalPathWithBranchRecorderDisabled(t *testing.T) {
	prev := ActiveBranchRecorder.Swap(nil)
	t.Cleanup(func() { ActiveBranchRecorder.Store(prev) })

	isolatedTpl, err := Compile(`{% if flag %}yes{% else %}no{% endif %}:{% for x in items %}{{ x }}{% empty %}empty{% endfor %}`)
	testutil.FailErr(t, "Compile isolated", err)

	got1, err := Execute(t.Context(), isolatedTpl, map[string]any{"flag": true, "items": []string{"a", "b"}})
	testutil.FailErr(t, "Execute isolated true", err)
	if got1 != "yes:ab" {
		t.Fatalf("got %q, want %q", got1, "yes:ab")
	}

	got2, err := ExecuteDetached(isolatedTpl, map[string]any{"flag": false, "items": []string{}})
	testutil.FailErr(t, "ExecuteDetached isolated false", err)
	if got2 != "no:empty" {
		t.Fatalf("got %q, want %q", got2, "no:empty")
	}

	loader := memTemplateLoader{
		"agents/coord.md":  `{% if ask %}ASK{% elif build %}BUILD{% else %}OTHER{% endif %}|{% include "partials/tail.md" %}`,
		"partials/tail.md": `{% if has_git %}git{% else %}nogit{% endif %}`,
	}
	set, err := NewSet("disabled-composed", loader, Composed)
	testutil.FailErr(t, "NewSet", err)
	composedTpl, err := set.FromFile("agents/coord.md")
	testutil.FailErr(t, "FromFile", err)

	got3, err := Execute(t.Context(), composedTpl, map[string]any{"ask": false, "build": true, "has_git": true})
	testutil.FailErr(t, "Execute composed", err)
	if got3 != "BUILD|git" {
		t.Fatalf("got %q, want %q", got3, "BUILD|git")
	}
	if ActiveBranchRecorder.Load() != nil {
		t.Fatal("ActiveBranchRecorder unexpectedly non-nil on disabled path")
	}
}

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
	prev := ActiveBranchRecorder.Swap(rec)
	t.Cleanup(func() { ActiveBranchRecorder.Store(prev) })

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

func TestBranchRecorderAttributesConcurrentComposedAndInlineRenders(t *testing.T) {
	loader := memTemplateLoader{
		"agents/alpha.md":    `{% if alpha_flag %}A{% else %}a{% endif %}:{% include "partials/shared.md" %}`,
		"agents/beta.md":     `{% if beta_flag %}B{% else %}b{% endif %}:{% include "partials/shared.md" %}`,
		"partials/shared.md": `{% if shared_flag %}S{% else %}s{% endif %}`,
	}
	set, err := NewSet("concurrent-composed", loader, Composed)
	testutil.FailErr(t, "NewSet", err)
	alphaTpl, err := set.FromFile("agents/alpha.md")
	testutil.FailErr(t, "FromFile alpha", err)
	betaTpl, err := set.FromFile("agents/beta.md")
	testutil.FailErr(t, "FromFile beta", err)

	inlineSrcA := `{% if inline_a %}IA{% else %}ia{% endif %}`
	inlineSrcB := `{% for v in vals %}{{ v }}{% empty %}none{% endfor %}`
	inlineTplA, err := Compile(inlineSrcA)
	testutil.FailErr(t, "Compile inline A", err)
	inlineTplB, err := Compile(inlineSrcB)
	testutil.FailErr(t, "Compile inline B", err)

	rec := &BranchRecorder{
		Aliases: map[string]string{
			InlineTemplateID(inlineSrcA): "guidance/inline-a.md",
		},
		Hits: map[string]*BranchHit{},
	}
	prev := ActiveBranchRecorder.Swap(rec)
	t.Cleanup(func() { ActiveBranchRecorder.Store(prev) })

	// Register inline B alias under Mu before launching readers of B while A/alpha/beta run.
	rec.Mu.Lock()
	rec.Aliases[InlineTemplateID(inlineSrcB)] = "guidance/inline-b.md"
	rec.Mu.Unlock()

	const workersPerKind = 16
	var wg sync.WaitGroup
	errCh := make(chan error, workersPerKind*4)

	for i := range workersPerKind {
		wg.Add(4)
		go func(idx int) {
			defer wg.Done()
			_, e := Execute(t.Context(), alphaTpl, map[string]any{
				"alpha_flag":  idx%2 == 0,
				"shared_flag": true,
			})
			if e != nil {
				errCh <- e
			}
		}(i)
		go func(idx int) {
			defer wg.Done()
			_, e := Execute(t.Context(), betaTpl, map[string]any{
				"beta_flag":   idx < 4,
				"shared_flag": false,
			})
			if e != nil {
				errCh <- e
			}
		}(i)
		go func(idx int) {
			defer wg.Done()
			_, e := Execute(t.Context(), inlineTplA, map[string]any{
				"inline_a": idx%4 == 0,
			})
			if e != nil {
				errCh <- e
			}
		}(i)
		go func(idx int) {
			defer wg.Done()
			vals := []string{"x"}
			if idx%2 == 1 {
				vals = nil
			}
			_, e := Execute(t.Context(), inlineTplB, map[string]any{"vals": vals})
			if e != nil {
				errCh <- e
			}
		}(i)
	}
	wg.Wait()
	close(errCh)
	for e := range errCh {
		testutil.FailErr(t, "concurrent Execute", e)
	}

	assertBranchCounts := func(branchID string, wantTrue, wantFalse int) {
		t.Helper()
		rec.Mu.Lock()
		hit := rec.Hits[branchID]
		rec.Mu.Unlock()
		if hit == nil {
			t.Fatalf("missing hit for %s (hits=%+v)", branchID, rec.Hits)
		}
		if hit.TakenTrue != wantTrue || hit.TakenFalse != wantFalse {
			t.Fatalf("%s counts = (true=%d, false=%d), want (true=%d, false=%d)",
				branchID, hit.TakenTrue, hit.TakenFalse, wantTrue, wantFalse)
		}
	}

	assertBranchCounts("agents/alpha.md@L1:C4:if", 8, 8)
	assertBranchCounts("agents/beta.md@L1:C4:if", 4, 12)
	assertBranchCounts("partials/shared.md@L1:C4:if", 16, 16)
	assertBranchCounts("guidance/inline-a.md@L1:C4:if", 4, 12)
	assertBranchCounts("guidance/inline-b.md@L1:C4:for", 8, 8)
}
