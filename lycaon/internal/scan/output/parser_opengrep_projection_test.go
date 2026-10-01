package output

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/scan/sourceview"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestOpengrepPartialParsingPreservesStructuredPosition(t *testing.T) {
	for _, kind := range []string{`"PartialParsing"`, `["PartialParsing",[]]`} {
		report := `{"results":[],"errors":[{"type":` + kind + `,"path":"page.html.script-0.js","message":"syntax error","spans":[{"file":"page.html.script-0.js","start":{"line":4,"col":6}},{"file":"page.html.script-0.js","start":{"line":2,"col":3}}]}]}`
		result, err := ParseOpengrepJSON(strings.NewReader(report))
		testutil.FailErr(t, "parse positioned syntax warning", err)
		if len(result.Warnings) != 1 || result.Warnings[0].StartLine != 2 || result.Warnings[0].StartColumn != 3 {
			t.Fatalf("syntax warning position=%+v", result.Warnings)
		}
	}
}

func TestOpengrepPartialParsingRejectsInconsistentSpan(t *testing.T) {
	for _, span := range []string{
		`{"file":"other.js","start":{"line":2,"col":3}}`,
		`{"file":"a.js","start":{"line":0,"col":3}}`,
		`{"file":"a.js","start":{"line":2,"col":0}}`,
	} {
		var diagnostic opengrepJSONErr
		if err := json.Unmarshal([]byte(`{"type":"PartialParsing","path":"a.js","spans":[`+span+`]}`), &diagnostic); err == nil {
			t.Fatalf("accepted inconsistent syntax location: %s", span)
		}
	}
}

func TestOpengrepProjectedSyntaxWarningUsesOriginalCoordinates(t *testing.T) {
	project, temporary := t.TempDir(), t.TempDir()
	projection := filepath.Join(temporary, "page.html.script-0.js")
	source := []byte("<div>狼</div>\r\n<script>const value = 1;\r\nif (\r\n</script>")
	scripts, limitations, err := sourceview.Scripts(t.Context(), source, false)
	testutil.FailErr(t, "project syntax error", err)
	if len(scripts) != 1 || len(limitations) != 0 {
		t.Fatalf("projection=%+v limitations=%+v", scripts, limitations)
	}
	for _, positioned := range []bool{false, true} {
		warning := api.ScanWarning{Kind: api.ScanWarningFilePartialParse, File: projection,
			Message: "Syntax error at line " + projection + ":2: `if (` was unexpected"}
		if positioned {
			warning.StartLine, warning.StartColumn = 2, 1
		}
		result := &Result{Warnings: []api.ScanWarning{warning}}
		testutil.FailErr(t, "map syntax warning", RemapOpengrepSources(result, map[string]sourceview.Origin{projection: {Path: "page.html", Map: scripts[0].Map}}, project))
		got := result.Warnings[0]
		if got.File != "page.html" || strings.Contains(got.Message, temporary) || strings.Contains(got.Message, "script-0") {
			t.Fatalf("temporary syntax location escaped: %+v", got)
		}
		if positioned && (got.StartLine != 3 || got.StartColumn != 1) || !positioned && (got.StartLine != 0 || got.StartColumn != 0) {
			t.Fatalf("original syntax position=%+v", got)
		}
	}
}
