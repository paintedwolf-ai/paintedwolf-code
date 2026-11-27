package oarcore

import (
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
)

func TestParseCopyExportRejectsIllegalConstructs(t *testing.T) {
	_, err := ParseCopy("{% for p in paths %}{{ p }}{% endfor %}")
	if err == nil || !strings.Contains(err.Error(), "for") {
		t.Fatalf("ParseCopy err=%v, want token for", err)
	}
}

func TestRenderCopyExportJoinsListsAndOmitsZero(t *testing.T) {
	got := RenderCopy(
		"{{ tool }}: {{ fields }}{% if arg_validation_reason %} — {{ arg_validation_reason }}{% endif %}",
		map[string]any{
			"tool":                  "command",
			"fields":                []string{"host_resources", "direct_ip"},
			"arg_validation_reason": "",
		},
	)
	if got != "command: host_resources, direct_ip" {
		t.Fatalf("render = %q", got)
	}
	got = RenderCopy("{{ fields }}", map[string]any{"fields": []any{"layout_overview", "secrets"}})
	if got != "layout_overview, secrets" {
		t.Fatalf("[]any render = %q", got)
	}
}

func TestParseCopyCollectsBindingsAndConditionals(t *testing.T) {
	refs, err := ParseCopy("Use {{ tool }}{% if arg_validation_reason %} — {{ arg_validation_reason }}{% endif %}")
	testutil.FailErr(t, "parse copy", err)
	got := map[string]bool{}
	for _, r := range refs {
		got[r.Name] = r.Interpolate
	}
	if !got["tool"] {
		t.Fatalf("tool should interpolate, refs=%v", refs)
	}
	if _, ok := got["arg_validation_reason"]; !ok {
		t.Fatalf("missing arg_validation_reason, refs=%v", refs)
	}
}

func TestParseCopyAcceptsDottedHostNames(t *testing.T) {
	refs, err := ParseCopy("{{host.worker_leg}} and {{ host.worker_leg }}")
	testutil.FailErr(t, "parse copy", err)
	if len(refs) != 1 || refs[0].Name != "host.worker_leg" {
		t.Fatalf("refs = %+v, want one host.worker_leg binding", refs)
	}
}

func TestParseCopyRejectsTokens(t *testing.T) {
	cases := []struct {
		source string
		token  string
	}{
		{"{% for p in paths %}{{ p }}{% endfor %}", "for"},
		{"{% include \"x\" %}", "include"},
		{"{{ tool extra }}", "binding"},
		// [OAR-COPY-3] Copy branches test facts, not expressions.
		{"{% if first and second %}x{% endif %}", "if"},
		{"{% if first == second %}x{% endif %}", "if"},
	}
	for _, tc := range cases {
		_, err := ParseCopy(tc.source)
		if err == nil {
			t.Fatalf("ParseCopy(%q) succeeded, want token %q", tc.source, tc.token)
		}
		if !strings.Contains(err.Error(), tc.token) {
			t.Fatalf("ParseCopy(%q) error %q does not contain %q", tc.source, err.Error(), tc.token)
		}
	}
}

func TestCountCopyBranchesLocatesBindingsAgainstGuard(t *testing.T) {
	cases := []struct {
		source  string
		binding string
		want    CopyBranches
	}{
		{"{% if load %}{{ load }}, then {% endif %}{{ call }}", "load", CopyBranches{WhenSet: 1}},
		{"{% if load %}{{ load }}, then {% endif %}{{ call }}", "call", CopyBranches{Either: 1}},
		{"{% if not load %}{{ call }}{% else %}{{ load }} and {{ call }}{% endif %}", "call", CopyBranches{WhenSet: 1, WhenEmpty: 1}},
		{"{{ load }}{% if other %}{% if load %}{{ load }}{% endif %}{% endif %}", "load", CopyBranches{WhenSet: 1, Either: 1}},
		{"{% if load %}{% if not load %}{{ load }}{% endif %}{% endif %}", "load", CopyBranches{WhenSet: 1}},
	}
	for _, tc := range cases {
		got, err := CountCopyBranches(tc.source, "load")
		testutil.FailErr(t, "count branches", err)
		if got[tc.binding] != tc.want {
			t.Fatalf("CountCopyBranches(%q)[%s] = %+v, want %+v", tc.source, tc.binding, got[tc.binding], tc.want)
		}
	}
	if _, err := CountCopyBranches("{% if load %}x", "load"); err == nil {
		t.Fatal("unterminated conditional counted, want a parse error")
	}
}

func TestRenderCopySubstitutesListsAndOmitsZeroIfs(t *testing.T) {
	lookup := func(name string) value {
		switch name {
		case "tool":
			return "command"
		case "arg_validation_reason":
			return ""
		case "fields":
			return []string{"host_resources", "direct_ip"}
		default:
			return ""
		}
	}
	got := renderCopy("{{ tool }}: {{ fields }}{% if arg_validation_reason %} — {{ arg_validation_reason }}{% endif %}", lookup)
	if got != "command: host_resources, direct_ip" {
		t.Fatalf("render = %q", got)
	}
}

func TestRenderCopyHonorsIfElseNot(t *testing.T) {
	flag := func(name string) value {
		if name == "flag" {
			return true
		}
		return ""
	}
	empty := func(name string) value {
		if name == "flag" {
			return false
		}
		return ""
	}
	if got := renderCopy("{% if flag %}yes{% else %}no{% endif %}", flag); got != "yes" {
		t.Fatalf("if true = %q", got)
	}
	if got := renderCopy("{% if not flag %}yes{% else %}no{% endif %}", flag); got != "no" {
		t.Fatalf("if not true = %q", got)
	}
	if got := renderCopy("{% if flag %}yes{% else %}no{% endif %}", empty); got != "no" {
		t.Fatalf("if false = %q", got)
	}
}
