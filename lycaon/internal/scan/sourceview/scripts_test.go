package sourceview

import (
	"bytes"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
)

func TestScriptsPreserveCoordinates(t *testing.T) {
	source := []byte("<template>狼\n<div>text</div></template>\n<script lang=\"ts\">\nconst value: string = location.hash;\neval(value);\n</script>\n")
	projections, _, err := Scripts(t.Context(), source, true)
	testutil.FailErr(t, "project scripts", err)
	got := projections[0].Source
	if !bytes.Equal(got, []byte("\nconst value: string = location.hash;\neval(value);\n")) {
		t.Fatalf("script bytes changed: %q", got)
	}
	start, end, ok := projections[0].Map.MapSpan(Position{Line: 2, Column: 1}, Position{Line: 3, Column: 13})
	if !ok || start != (Position{Line: 4, Column: 1}) || end != (Position{Line: 5, Column: 13}) {
		t.Fatalf("mapped span: %+v %+v %v", start, end, ok)
	}
	if bytes.Contains(got, []byte("template")) {
		t.Fatal("markup leaked into script")
	}
}

func TestProjectionLanguageFollowsItsContainer(t *testing.T) {
	for _, tc := range []struct {
		attributes, extension string
		component             bool
	}{
		{`lang="ts"`, "js", false},
		{`lang="en"`, "js", false},
		{`lang="ts"`, "ts", true},
		{`lang="jsx"`, "jsx", true},
		{`lang="tsx"`, "tsx", true},
		{`type="application/json"`, "js", true},
		{`src=""`, "js", true},
		{`setup`, "js", true},
	} {
		projected, _, err := Scripts(t.Context(), []byte("<script "+tc.attributes+">const value = 1;</script>"), tc.component)
		testutil.FailErr(t, "project language", err)
		if len(projected) != 1 || projected[0].Extension != tc.extension {
			t.Fatalf("component=%v attributes=%s projected=%+v", tc.component, tc.attributes, projected)
		}
	}
}

func TestVueScriptRejectsUnsupportedCompilerInputs(t *testing.T) {
	for _, attributes := range []string{
		`lang="TS"`, `lang=" ts "`, `lang="typescript"`,
		`lang="js" lang="ts"`, `setup src="./setup.js"`,
	} {
		t.Run(attributes, func(t *testing.T) {
			_, _, err := Scripts(t.Context(), []byte("<script "+attributes+">eval(location.hash)</script>"), true)
			if err == nil {
				t.Fatal("unsupported component appeared fully analyzed")
			}
		})
	}
}

func TestScriptsMatchHTMLTypeSemantics(t *testing.T) {
	for _, tc := range []struct {
		attributes string
		executable bool
	}{
		{`type="text/javascript; charset=utf-8"`, false},
		{`type="module; charset=utf-8"`, false},
		{`type=" "`, false},
		{"type=\"\u00a0module\u00a0\"", false},
		{`type="text/javascrİpt"`, false},
		{`type="application/json" type="module"`, false},
		{`language="vbscript"`, false},
		{`type="" language="vbscript"`, true},
		{`language="JavaScript1.2"`, true},
		{`lang="en"`, true},
		{`type="application/x-javascript"`, true},
		{`type=" MODULE "`, true},
	} {
		t.Run(tc.attributes, func(t *testing.T) {
			projected, _, err := Scripts(t.Context(), []byte("<script "+tc.attributes+">eval(location.hash)</script>"), false)
			testutil.FailErr(t, "project script type", err)
			if (len(projected) > 0) != tc.executable {
				t.Fatalf("projected=%d executable=%v", len(projected), tc.executable)
			}
		})
	}
	projected, _, err := Scripts(t.Context(), []byte(`<script type="text/javascript" type="module">let value = location.hash;</script><script>eval(value)</script>`), false)
	testutil.FailErr(t, "project duplicate attribute scopes", err)
	if len(projected) != 1 || !bytes.Contains(projected[0].Source, []byte("location.hash")) || !bytes.Contains(projected[0].Source, []byte("eval(value)")) {
		t.Fatal("duplicate type attribute changed the first attribute's classic scope")
	}
}

func TestScriptsRespectHTMLExecutionBoundaries(t *testing.T) {
	for _, source := range []string{
		"<!-- <script>eval(location.hash)</script> -->",
		"<script type=\"application/ld+json\">{\"example\":\"eval(location.hash)\"}</script>",
		"<script type=\"application/json\">{}</script>",
		"<script src=\"app.js\">eval(location.hash)</script>",
		"<textarea><script>eval(location.hash)</script></textarea>",
	} {
		t.Run(source, func(t *testing.T) {
			got, _, err := Scripts(t.Context(), []byte(source), false)
			testutil.FailErr(t, "project", err)
			if len(got) > 0 {
				t.Fatalf("non-executable block projected: %+v", got)
			}
		})
	}
	source := "<script>const text = '<!--';</script><script setup lang=\"ts\">eval(location.hash)</script>"
	got, _, err := Scripts(t.Context(), []byte(source), false)
	testutil.FailErr(t, "multiple scripts", err)
	if !strings.Contains(string(got[0].Source), "const text") || !strings.Contains(string(got[0].Source), "eval(location.hash)") {
		t.Fatalf("lost executable regions: %+v", got)
	}
	if _, _, err := Scripts(t.Context(), []byte("<script lang=\"coffee\">value = 1</script>"), true); err == nil {
		t.Fatal("unsupported script language appeared clean")
	}
}

func TestScriptsKeepModuleBindingsSeparate(t *testing.T) {
	source := []byte("<script type=\"module\">let value = location.hash;</script>\r\n<script type=\"module\">let value = 'fixed'; eval(value);</script>")
	projections, _, err := Scripts(t.Context(), source, false)
	testutil.FailErr(t, "project independent modules", err)
	if len(projections) != 2 {
		t.Fatalf("modules = %d", len(projections))
	}
	if bytes.Contains(projections[1].Source, []byte("location.hash")) || bytes.Contains(projections[0].Source, []byte("eval")) {
		t.Fatal("module bindings were combined")
	}
	for i, projection := range projections {
		start, ok := projection.Map.MapPosition(Position{Line: 1, Column: 1})
		if !ok || start != (Position{Line: i + 1, Column: 23}) {
			t.Fatalf("module origin=%+v valid=%v", start, ok)
		}
	}
	component, _, err := Scripts(t.Context(), []byte("<script>const value = location.hash;</script><script setup>const value = 'fixed'; eval(value);</script>"), true)
	testutil.FailErr(t, "project component scopes", err)
	if len(component) != 2 {
		t.Fatal("component scripts merged")
	}
	if _, _, err := Scripts(t.Context(), []byte("<script>eval(location.hash)"), false); err == nil {
		t.Fatal("unterminated script appeared complete")
	}
}
