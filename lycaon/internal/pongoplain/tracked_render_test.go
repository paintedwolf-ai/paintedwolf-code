package pongoplain

import (
	"errors"
	"testing"

	"github.com/flosch/pongo2/v6"
	"github.com/lycaon/lycaon/internal/testutil"
)

func renderIsolated(t *testing.T, src string, data map[string]any) (string, error) {
	t.Helper()
	tpl, err := Compile(src)
	if err != nil {
		return "", err
	}
	return Execute(t.Context(), tpl, data)
}

func composedTemplate(t *testing.T, files memTemplateLoader, name string) (*pongo2.Template, error) {
	t.Helper()
	set, err := NewSet("composed-"+t.Name(), files, Composed)
	testutil.FailErr(t, "NewSet", err)
	return set.FromFile(name)
}

func originalError(err error) error {
	var pErr *pongo2.Error
	if errors.As(err, &pErr) {
		return pErr.OrigError
	}
	return err
}

func TestTrackedTagsRenderTheirPongoMeaning(t *testing.T) {
	for _, tc := range []struct {
		name, src string
		data      map[string]any
		want      string
	}{
		{"if arm", `{% if a %}A{% elif b %}B{% else %}C{% endif %}`, map[string]any{"a": true}, "A"},
		{"elif arm", `{% if a %}A{% elif b %}B{% else %}C{% endif %}`, map[string]any{"b": true}, "B"},
		{"else arm", `{% if a %}A{% elif b %}B{% else %}C{% endif %}`, nil, "C"},
		{"no arm", `{% if a %}A{% endif %}`, nil, ""},
		{"loop counters",
			`{% for x in xs %}{{ forloop.Counter }}{{ forloop.Counter0 }}{{ forloop.Revcounter }}{{ forloop.Revcounter0 }}` +
				`{% if forloop.First %}F{% endif %}{% if forloop.Last %}L{% endif %};{% endfor %}`,
			map[string]any{"xs": []string{"a", "b"}}, "1021F;2110L;"},
		{"parent loop", `{% for x in xs %}{% for y in ys %}{{ forloop.Parentloop.Counter }}{{ forloop.Counter }} {% endfor %}{% endfor %}`,
			map[string]any{"xs": []int{1, 2}, "ys": []int{1}}, "11 21 "},
		{"map pairs", `{% for k, v in m sorted %}{{ k }}={{ v }} {% endfor %}`, map[string]any{"m": map[string]int{"b": 2, "a": 1}}, "a=1 b=2 "},
		{"reversed", `{% for x in xs reversed %}{{ x }}{% endfor %}`, map[string]any{"xs": []int{1, 2, 3}}, "321"},
		{"sorted reversed keys", `{% for k in m reversed sorted %}{{ k }}{% endfor %}`, map[string]any{"m": map[string]int{"a": 1, "c": 3, "b": 2}}, "cba"},
		{"empty", `{% for x in xs %}{{ x }}{% empty %}none{% endfor %}`, map[string]any{"xs": []int{}}, "none"},
		{"set", `{% set y = a %}{{ y }}`, map[string]any{"a": "v"}, "v"},
		{"with pairs", `{% with p=a q=b %}{{ p }}{{ q }}{% endwith %}{{ p }}`, map[string]any{"a": 1, "b": 2}, "12"},
		{"with as", `{% with a as p b as q %}{{ p }}{{ q }}{% endwith %}`, map[string]any{"a": 1, "b": 2}, "12"},
		{"ifequal", `{% ifequal a b %}eq{% else %}ne{% endifequal %}`, map[string]any{"a": 1, "b": 1}, "eq"},
		{"ifequal else", `{% ifequal a b %}eq{% else %}ne{% endifequal %}`, map[string]any{"a": 1, "b": 2}, "ne"},
		{"ifequal miss", `{% ifequal a b %}eq{% endifequal %}`, map[string]any{"a": 1, "b": 2}, ""},
		{"ifnotequal", `{% ifnotequal a b %}ne{% else %}eq{% endifnotequal %}`, map[string]any{"a": 1, "b": 2}, "ne"},
		{"ifnotequal else", `{% ifnotequal a b %}ne{% else %}eq{% endifnotequal %}`, map[string]any{"a": 1, "b": 1}, "eq"},
		{"firstof", `{% firstof a b "c" %}`, map[string]any{"a": "", "b": "B"}, "B"},
		{"firstof none", `{% firstof a b %}`, nil, ""},
		{"firstof escaped", `{% autoescape on %}{% firstof a %}{% endautoescape %}`, map[string]any{"a": "<x>"}, "&lt;x&gt;"},
		{"firstof safe", `{% autoescape on %}{% firstof a|safe %}{% endautoescape %}`, map[string]any{"a": "<x>"}, "<x>"},
		{"widthratio", `{% widthratio 5 10 100 %}`, nil, "51"},
		{"widthratio as", `{% widthratio 5 10 100 as w %}[{{ w }}]`, nil, "[51]"},
		{"widthratio zero", `{% widthratio 5 0 100 %}`, nil, ""},
		{"widthratio zero as", `{% widthratio 5 0 100 as w %}[{{ w }}]`, nil, "[]"},
		{"autoescape nesting", `{% autoescape on %}{{ a }}{% autoescape off %}{{ a }}{% endautoescape %}{{ a }}{% endautoescape %}{{ a }}`,
			map[string]any{"a": "<"}, "&lt;<&lt;<"},
	} {
		out, err := renderIsolated(t, tc.src, tc.data)
		if err != nil {
			t.Errorf("%s: render: %v", tc.name, err)
			continue
		}
		if out != tc.want {
			t.Errorf("%s: rendered %q, want %q", tc.name, out, tc.want)
		}
	}
}

func TestTrackedTagsRefuseMalformedArguments(t *testing.T) {
	for _, src := range []string{
		`{% if %}x{% endif %}`,
		`{% if a b %}x{% endif %}`,
		`{% if a %}x`,
		`{% if a %}{% elif %}{% endif %}`,
		`{% if a %}{% elif a b %}{% endif %}`,
		`{% if a %}{% else x %}{% endif %}`,
		`{% if a %}{% endif x %}`,
		`{% for %}{% endfor %}`,
		`{% for x, %}{% endfor %}`,
		`{% for x y %}{% endfor %}`,
		`{% for x in %}{% endfor %}`,
		`{% for x in xs foo %}{% endfor %}`,
		`{% for x in xs %}`,
		`{% for x in xs %}{% endfor x %}`,
		`{% for x in xs %}{% empty x %}{% endfor %}`,
		`{% for x in xs %}{% empty %}`,
		`{% for x in xs %}{% empty %}{% endfor x %}`,
		`{% set %}`,
		`{% set x %}`,
		`{% set x = %}`,
		`{% set x = a b %}`,
		`{% with %}{% endwith %}`,
		`{% with a=1 %}`,
		`{% with a=1 %}{% endwith x %}`,
		`{% with a as %}{% endwith %}`,
		`{% with a b as c %}{% endwith %}`,
		`{% with as c %}{% endwith %}`,
		`{% with 1=2 %}{% endwith %}`,
		`{% with a 2 %}{% endwith %}`,
		`{% with a= %}{% endwith %}`,
		`{% ifequal %}{% endifequal %}`,
		`{% ifequal a %}{% endifequal %}`,
		`{% ifequal a b c %}{% endifequal %}`,
		`{% ifequal a b %}`,
		`{% ifequal a b %}{% endifequal x %}`,
		`{% ifequal a b %}{% else x %}{% endifequal %}`,
		`{% ifequal a b %}{% else %}`,
		`{% ifequal a b %}{% else %}{% endifequal x %}`,
		`{% firstof a | %}`,
		`{% widthratio %}`,
		`{% widthratio 1 %}`,
		`{% widthratio 1 2 %}`,
		`{% widthratio 1 2 3 as %}`,
		`{% widthratio 1 2 3 4 %}`,
		`{% autoescape %}{% endautoescape %}`,
		`{% autoescape maybe %}{% endautoescape %}`,
		`{% autoescape on x %}{% endautoescape %}`,
		`{% autoescape on %}`,
		`{% autoescape on %}{% endautoescape x %}`,
	} {
		if _, err := Compile(src); err == nil {
			t.Errorf("Compile(%q) succeeded, want a parse refusal", src)
		}
	}
}

func TestTrackedTagsPropagateEvaluationFailures(t *testing.T) {
	// The date filter fails at render time on a string input.
	const bad = `d|date:"2006"`
	data := map[string]any{"d": "not a time", "xs": []int{1}, "nothing": []int{}}
	for _, src := range []string{
		`{% if ` + bad + ` %}{% endif %}`,
		`{% if false %}{% elif ` + bad + ` %}{% endif %}`,
		`{% for x in ` + bad + ` %}{% endfor %}`,
		`{% for x in xs %}{{ ` + bad + ` }}{% endfor %}`,
		`{% for x in nothing %}{% empty %}{{ ` + bad + ` }}{% endfor %}`,
		`{% set y = ` + bad + ` %}`,
		`{% with y=` + bad + ` %}{% endwith %}`,
		`{% ifequal ` + bad + ` 1 %}{% endifequal %}`,
		`{% ifequal 1 ` + bad + ` %}{% endifequal %}`,
		`{% firstof ` + bad + ` %}`,
		`{% widthratio ` + bad + ` 1 1 %}`,
		`{% widthratio 1 ` + bad + ` 1 %}`,
		`{% widthratio 1 1 ` + bad + ` %}`,
	} {
		out, err := renderIsolated(t, src, data)
		if err == nil || out != "" {
			t.Errorf("render %q = %q, %v; want an evaluation error and no output", src, out, err)
		}
	}
}

func TestTrackedTagsRefuseToRenderOutsideExecute(t *testing.T) {
	var templates []*pongo2.Template
	for _, src := range []string{
		`{% if a %}{% endif %}`,
		`{% for x in xs %}{% endfor %}`,
		`{% set y = 1 %}`,
		`{% with y=1 %}{% endwith %}`,
		`{% ifequal 1 1 %}{% endifequal %}`,
		`{% firstof a %}`,
		`{% widthratio 1 2 3 %}`,
		`{% autoescape on %}{% endautoescape %}`,
	} {
		tpl, err := Compile(src)
		testutil.FailErr(t, "compile "+src, err)
		templates = append(templates, tpl)
	}
	for _, src := range []string{`{% include "b.md" %}`, `{% include "gone.md" if_exists %}`} {
		tpl, err := composedTemplate(t, memTemplateLoader{"a.md": src, "b.md": "x"}, "a.md")
		testutil.FailErr(t, "compose "+src, err)
		templates = append(templates, tpl)
	}
	for _, tpl := range templates {
		out, err := tpl.Execute(nil)
		if !errors.Is(originalError(err), ErrExecutionLimit) {
			t.Errorf("unbounded render = %q, %v; want ErrExecutionLimit", out, err)
		}
	}
}

func TestZeroOutputTagsSpendTheStepBudget(t *testing.T) {
	data := map[string]any{"xs": make([]int, MaxCollectionItems)}
	loop := func(body string) string {
		return `{% for a in xs %}{% for b in xs %}` + body + `{% endfor %}{% endfor %}`
	}
	if _, err := renderIsolated(t, loop(""), data); err != nil {
		t.Fatalf("bare nested loop must fit the budget: %v", err)
	}
	for _, body := range []string{
		`{% ifequal a b %}{% endifequal %}`,
		`{% ifnotequal a b %}{% endifnotequal %}`,
		`{% firstof "" %}`,
		`{% widthratio a 0 1 %}`,
		`{% autoescape off %}{% endautoescape %}`,
		`{% with c=a %}{% endwith %}`,
	} {
		if _, err := renderIsolated(t, loop(body), data); !errors.Is(err, ErrExecutionLimit) {
			t.Errorf("%s in a full nested loop: err = %v, want ErrExecutionLimit", body, err)
		}
	}
	tpl, err := composedTemplate(t, memTemplateLoader{"a.md": loop(`{% include "gone.md" if_exists %}`)}, "a.md")
	testutil.FailErr(t, "compose", err)
	if _, err := Execute(t.Context(), tpl, data); !errors.Is(err, ErrExecutionLimit) {
		t.Errorf("missing optional include in a full nested loop: err = %v, want ErrExecutionLimit", err)
	}
}

func TestIncludeBindsItsContextAndRefusesUnsafeComposition(t *testing.T) {
	files := memTemplateLoader{
		"with.md":     `{% include "b.md" with n=a %}`,
		"only.md":     `{% include "b.md" with n=a only %}`,
		"bare.md":     `{% include "b.md" only %}`,
		"optional.md": `{% include "missing.md" if_exists %}x`,
		"b.md":        `{{ n }}{{ a }}`,
	}
	for name, want := range map[string]string{"with.md": "vv", "only.md": "v", "bare.md": "", "optional.md": "x"} {
		tpl, err := composedTemplate(t, files, name)
		testutil.FailErr(t, "compose "+name, err)
		out, err := Execute(t.Context(), tpl, map[string]any{"a": "v"})
		testutil.FailErr(t, "render "+name, err)
		if out != want {
			t.Errorf("%s rendered %q, want %q", name, out, want)
		}
	}

	for name, files := range map[string]memTemplateLoader{
		"included body fails": {"a.md": `{% include "b.md" %}`, "b.md": `{{ d|date:"2006" }}`},
		"binding fails":       {"a.md": `{% include "b.md" with n=d|date:"2006" %}`, "b.md": `x`},
	} {
		tpl, err := composedTemplate(t, files, "a.md")
		testutil.FailErr(t, "compose "+name, err)
		if out, err := Execute(t.Context(), tpl, map[string]any{"d": "x"}); err == nil || out != "" {
			t.Errorf("%s: rendered %q, %v; want an evaluation error", name, out, err)
		}
	}

	for name, src := range map[string]string{
		"empty target":     `{% include "" %}`,
		"bare with":        `{% include "b.md" with %}`,
		"with non-name":    `{% include "b.md" with 1=2 %}`,
		"with without =":   `{% include "b.md" with a 2 %}`,
		"with bad value":   `{% include "b.md" with a= %}`,
		"trailing clause":  `{% include "b.md" extra %}`,
		"missing required": `{% include "missing.md" %}`,
	} {
		if _, err := composedTemplate(t, memTemplateLoader{"a.md": src, "b.md": "x"}, "a.md"); err == nil {
			t.Errorf("%s: composed %q, want a parse refusal", name, src)
		}
	}
	if _, err := composedTemplate(t, memTemplateLoader{"a.md": `{% include "" %}`}, "a.md"); !errors.Is(originalError(err), ErrComposition) {
		t.Errorf("empty include target: err = %v, want ErrComposition", err)
	}
	if _, err := composedTemplate(t, memTemplateLoader{"a.md": `x{% include "a.md" %}`}, "a.md"); !errors.Is(originalError(err), ErrComposition) {
		t.Errorf("self include: err = %v, want ErrComposition", err)
	}

	raw := pongo2.NewSet("raw-"+t.Name(), memTemplateLoader{"a.md": `{% include "b.md" %}`, "b.md": "x"})
	if _, err := raw.FromFile("a.md"); !errors.Is(originalError(err), ErrComposition) {
		t.Errorf("include in a set built without NewSet: err = %v, want ErrComposition", err)
	}
}

func TestExecuteNormalizesUnsignedValuesAndContainsPanics(t *testing.T) {
	out, err := renderIsolated(t, `{{ a }}/{{ b }}`, map[string]any{"a": uint(7), "b": uint8(9)})
	testutil.FailErr(t, "render unsigned", err)
	if out != "7/9" {
		t.Fatalf("unsigned render = %q, want 7/9", out)
	}

	tpl, err := Compile(`{{ a }}`)
	testutil.FailErr(t, "compile", err)
	out, err = ExecuteWithStepHook(t.Context(), tpl, map[string]any{"a": "x"}, func(int) { panic("step hook failure") })
	if err == nil || out != "" || errors.Is(err, ErrExecutionLimit) {
		t.Fatalf("panicking render = %q, %v; want an internal execution error without output", out, err)
	}
}
