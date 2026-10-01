package output

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/pkg/api"
)

// knownWarningKinds lists diagnostics handled by the coverage projection.
var knownWarningKinds = map[api.ScanWarningKind]bool{
	api.ScanWarningRuleParseError:       true,
	api.ScanWarningFilePartialParse:     true,
	api.ScanWarningFilePartialSemantics: true,
	api.ScanWarningTargetUnscanned:      true,
}

// A map preserves case-sensitive keys when counting report diagnostics.
func reportedErrorCount(raw []byte) (int, bool) {
	var envelope map[string]json.RawMessage
	if err := json.Unmarshal(raw, &envelope); err != nil {
		return 0, false
	}
	body, declared := envelope["errors"]
	if !declared {
		return 0, true
	}
	var rows []json.RawMessage
	if err := json.Unmarshal(body, &rows); err != nil {
		return 0, false
	}
	return len(rows), true
}

func FuzzOpengrepJSONParser(f *testing.F) {
	seeds := []string{
		``,
		`{}`,
		`{"results":[]}`,
		`{"results":[{"check_id":"x","path":"a.go","start":{"line":1},"extra":{"severity":"WARNING","message":"m"}}]}`,
		`{"results":[],"errors":[{"message":"oops"}]}`,
		`{"results":[],"errors":[{"type":["PartialParsing",[{"path":"a.js"}]],"path":"a.js","message":"syntax error"}]}`,
		`null`,
		`[]`,
		`{"results":[{"check_id":"","path":"","start":{"line":-1},"extra":{}}]}`,
		// Findings and diagnostics coexist in partial reports.
		`{"results":[` + oneOpengrepFinding + `],"errors":[{"message":"boom","type":"SemgrepError"}]}`,
		// Diagnostics may have no message or type.
		`{"results":[` + oneOpengrepFinding + `],"errors":[{}]}`,
		`{"results":[` + oneOpengrepFinding + `],"errors":[{"message":"  ","type":"  "}]}`,
		`{"results":[` + oneOpengrepFinding + `],"errors":[{"detail":"timeout"}]}`,
		`{"results":[` + oneOpengrepFinding + `],"results":[` + oneOpengrepFinding + `]}`,
		`{"results":[],"errors":[{"type":["PartialSemantics","unsupported_ast_to_il"],"path":"a.py","message":"g","spans":[{"file":"a.py","start":{"line":1,"col":1}}]}]}`,
		`{"results":[{"check_id":"x","path":"a.go","start":{"line":1,"col":1},"end":{"line":1,"col":2},"extra":{"message":"m","severity":"ERROR","dataflow_trace":{"taint_source":["CliLoc",[{"path":"a.go","start":{"line":1,"col":1},"end":{"line":1,"col":2}},"src"]]}}}]}`,
	}
	for _, s := range seeds {
		f.Add([]byte(s))
	}
	parser := newOpengrepJSONParser()

	f.Fuzz(func(t *testing.T, raw []byte) {
		result, err := parser.Parse(raw)
		if err != nil {
			if result != nil {
				t.Fatalf("rejected report also returned a result: %+v", result)
			}
			if !strings.HasPrefix(err.Error(), "opengrep_json:") {
				t.Fatalf("parse failure lost its boundary label: %v", err)
			}
			return
		}
		if result == nil {
			t.Fatal("parse reported success with no result")
		}

		// Exit validation reads the finding count separately from the slice.
		if result.FindingsCount != len(result.Findings) {
			t.Fatalf("FindingsCount = %d with %d findings", result.FindingsCount, len(result.Findings))
		}
		if len(result.Categories) == 0 {
			t.Fatal("accepted report carries no categories, so it lands in no scan surface")
		}
		for _, warning := range result.Warnings {
			if !knownWarningKinds[warning.Kind] {
				t.Fatalf("warning kind %q is outside the coverage vocabulary, so no coverage rule reads it", warning.Kind)
			}
		}

		// The coverage projection depends on retained diagnostics.
		reported, ok := reportedErrorCount(raw)
		if ok && reported > 0 && len(result.Warnings) == 0 {
			t.Fatalf("report declared %d errors and the host kept no diagnostic: coverage = %s", reported, CoverageForResult(result, "exact", ""))
		}
	})
}
