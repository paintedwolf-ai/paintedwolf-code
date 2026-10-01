package output

import (
	"fmt"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/scan/opengrep"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

// oneOpengrepFinding isolates envelope validation from finding validation.
const oneOpengrepFinding = `{"check_id":"lycaon.go.a","path":"a.go","start":{"line":1,"col":1},"end":{"line":1,"col":2},"extra":{"message":"m","severity":"ERROR"}}`

func opengrepReport(results, errors string) string {
	return `{"paths":{"scanned":["a.go"]},"results":[` + results + `],"errors":[` + errors + `]}`
}

func TestTruncatedOpengrepReportNeverParses(t *testing.T) {
	t.Parallel()
	complete := opengrepReport(oneOpengrepFinding, "")
	for name, raw := range map[string]string{
		"empty file":           "",
		"whitespace only":      "   \n\t ",
		"open brace only":      "{",
		"cut mid key":          `{"resu`,
		"cut mid finding":      `{"results":[` + oneOpengrepFinding[:60],
		"cut after row comma":  `{"results":[` + oneOpengrepFinding + `,`,
		"unclosed results":     `{"results":[` + oneOpengrepFinding,
		"unclosed envelope":    `{"results":[` + oneOpengrepFinding + `]`,
		"cut mid errors":       `{"results":[],"errors":[{"message":"disk `,
		"truncated at half":    complete[:len(complete)/2],
		"truncated one byte":   complete[:len(complete)-1],
		"nul terminated":       complete[:len(complete)-1] + "\x00",
		"top level array":      `[]`,
		"top level null":       `null`,
		"top level string":     `"done"`,
		"trailing second doc":  complete + complete,
		"trailing junk":        complete + " garbage",
		"results is an object": `{"results":{}}`,
		"errors is an object":  `{"results":[],"errors":{}}`,
		// The results field is required even for an empty scan.
		"no results field": `{}`,
		"only paths":       `{"paths":{"scanned":["a.go"]}}`,
		"only errors":      `{"errors":[{"message":"boom","type":"E"}]}`,
		// Duplicate fields make the report ambiguous.
		"repeated results key":  `{"results":[` + oneOpengrepFinding + `],"results":[` + oneOpengrepFinding + `]}`,
		"repeated errors key":   `{"results":[],"errors":[{"message":"a","type":"E"}],"errors":[{"message":"b","type":"E"}]}`,
		"repeated paths key":    `{"paths":{"scanned":["a.go"]},"paths":{"scanned":[]},"results":[]}`,
		"repeated unknown key":  `{"version":"1","version":"2","results":[]}`,
		"non string object key": `{"results":[],` + "\n" + `1:2}`,
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			result, err := ParseOpengrepJSON(strings.NewReader(raw))
			if err == nil {
				t.Fatalf("a report the host could not read parsed as a scan: %d findings, %d warnings", len(result.Findings), len(result.Warnings))
			}
			if !strings.HasPrefix(err.Error(), "opengrep_json:") {
				t.Fatalf("parse failure lost its boundary label: %v", err)
			}
		})
	}
	// Exercise the catalog parser entry point with the same truncated bytes.
	parser := newOpengrepJSONParser()
	for _, raw := range []string{"", "{", complete[:len(complete)-1]} {
		if _, err := parser.Parse([]byte(raw)); err == nil {
			t.Fatalf("registry parser accepted a truncated report %q", raw)
		}
	}
}

// The run directory pre-creates report.json, so missing output is an empty file.
func TestEmptyReportIsNotAnEmptyScan(t *testing.T) {
	t.Parallel()
	if _, err := ParseOpengrepJSON(strings.NewReader("")); err == nil {
		t.Fatal("zero bytes parsed as a completed scan with no findings")
	}
	if _, err := newOpengrepJSONParser().Parse(nil); err == nil {
		t.Fatal("a nil report body parsed as a completed scan with no findings")
	}
	if _, err := ParseOpengrepJSON(nil); err == nil {
		t.Fatal("a nil reader parsed as a completed scan with no findings")
	}
}

// Coverage and exit validation both use the retained diagnostics.
func TestEveryReportedErrorSurvivesAsADiagnostic(t *testing.T) {
	t.Parallel()
	for name, errors := range map[string]string{
		"described fatal":     `{"message":"Invalid rule schema","type":"SemgrepError"}`,
		"typed but silent":    `{"type":"SemgrepError"}`,
		"message but untyped": `{"message":"could not open target"}`,
		"neither field set":   `{}`,
		"blank fields":        `{"message":"   ","type":"  "}`,
		"null fields":         `{"message":null,"type":null}`,
		"unknown shape":       `{"detail":"timeout reached","code":7}`,
		"three silent rows":   `{},{},{}`,
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			// A failed report without findings is rejected.
			if _, err := ParseOpengrepJSON(strings.NewReader(opengrepReport("", errors))); err == nil {
				t.Fatal("a report whose only content was a failure parsed as an empty scan")
			}
			if _, err := ParseOpengrepJSON(strings.NewReader(opengrepReport(oneOpengrepFinding, errors))); err == nil {
				t.Fatal("an invocation failure was hidden by unrelated findings")
			}
		})
	}
}

func TestReportExitGateAdmitsOnlyEstablishedOutcomes(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name                  string
		code, findings, diags int
		admit                 bool
	}{
		{name: "clean run", code: 0, admit: true},
		{name: "clean run with findings", code: 0, findings: 3, admit: true},
		{name: "findings exit carries findings", code: 1, findings: 1, admit: true},
		{name: "findings exit with nothing found", code: 1, admit: false},
		{name: "findings exit explained only by diagnostics", code: 1, diags: 4, admit: false},
		{name: "error exit with a diagnostic", code: 2, diags: 1, admit: true},
		{name: "error exit with findings but no diagnostic", code: 2, findings: 9, admit: false},
		{name: "error exit silent", code: 2, admit: false},
		{name: "top documented error exit", code: 5, diags: 1, admit: true},
		{name: "beyond documented range", code: 6, diags: 1, admit: false},
		{name: "timeout kill", code: 124, findings: 2, diags: 2, admit: false},
		{name: "sigkill oom", code: 137, findings: 2, diags: 2, admit: false},
		{name: "sigsegv", code: 139, diags: 1, admit: false},
		{name: "no exit status observed", code: -1, findings: 5, diags: 5, admit: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			err := opengrep.ValidateReportExit(tc.code, tc.findings, tc.diags)
			if tc.admit && err != nil {
				t.Fatalf("exit %d (findings=%d diagnostics=%d) rejected: %v", tc.code, tc.findings, tc.diags, err)
			}
			if !tc.admit && err == nil {
				t.Fatalf("exit %d (findings=%d diagnostics=%d) established a scan it does not evidence", tc.code, tc.findings, tc.diags)
			}
		})
	}
}

// Deduplication runs after parsing.
func TestDuplicateAndRepeatedContentIsCountedExactly(t *testing.T) {
	t.Parallel()
	for name, tc := range map[string]struct {
		raw  string
		want int
	}{
		"identical rows twice":  {raw: opengrepReport(oneOpengrepFinding+","+oneOpengrepFinding, ""), want: 2},
		"identical rows thrice": {raw: opengrepReport(strings.Repeat(oneOpengrepFinding+",", 2)+oneOpengrepFinding, ""), want: 3},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			result, err := ParseOpengrepJSON(strings.NewReader(tc.raw))
			testutil.FailErr(t, "parse repeated content", err)
			if len(result.Findings) != tc.want || result.FindingsCount != tc.want {
				t.Fatalf("kept %d findings with count %d, want %d of each", len(result.Findings), result.FindingsCount, tc.want)
			}
		})
	}
}

func TestMixedFindingsAndErrorsStayPartial(t *testing.T) {
	t.Parallel()
	result, err := ParseOpengrepJSON(strings.NewReader(opengrepReport(
		oneOpengrepFinding,
		`{"type":["PartialParsing",[{"path":"b.js"}]],"path":"b.js","message":"syntax error"},`+
			`{"type":"Rule parse error","rule_id":"lycaon.go.bad","message":"Rule parse error in rule lycaon.go.bad: `+"`"+`$X(...)`+"`"+`"},`+
			`{"message":"target exceeded memory","type":"Out of memory","path":"c.go"}`)))
	testutil.FailErr(t, "parse mixed report", err)
	if result.FindingsCount != 1 {
		t.Fatalf("mixed report kept %d findings, want 1", result.FindingsCount)
	}
	kinds := map[api.ScanWarningKind]int{}
	for _, w := range result.Warnings {
		kinds[w.Kind]++
	}
	for _, want := range []api.ScanWarningKind{api.ScanWarningFilePartialParse, api.ScanWarningRuleParseError, api.ScanWarningTargetUnscanned} {
		if kinds[want] == 0 {
			t.Fatalf("mixed report dropped every %s diagnostic: %+v", want, result.Warnings)
		}
	}
	if got := CoverageForResult(result, "exact", ""); got != api.ScanCoveragePartial {
		t.Fatalf("mixed report reported coverage %s, want %s", got, api.ScanCoveragePartial)
	}
}

func TestLargeReportIsCountedRatherThanSilentlyShortened(t *testing.T) {
	t.Parallel()
	const rows = 50000
	var b strings.Builder
	b.WriteString(`{"results":[`)
	for i := range rows {
		if i > 0 {
			b.WriteByte(',')
		}
		fmt.Fprintf(&b, `{"check_id":"lycaon.go.r%d","path":"f%d.go","start":{"line":1,"col":1},"end":{"line":1,"col":2},"extra":{"message":"m","severity":"ERROR"}}`, i, i)
	}
	b.WriteString(`]}`)
	result, err := ParseOpengrepJSON(strings.NewReader(b.String()))
	testutil.FailErr(t, "parse large report", err)
	if result.FindingsCount != rows || len(result.Findings) != rows {
		t.Fatalf("large report kept %d findings with count %d, want %d of each", len(result.Findings), result.FindingsCount, rows)
	}
}

func TestUnusableFindingShapesAreRefused(t *testing.T) {
	t.Parallel()
	row := func(checkID, path string, sl, sc, el, ec int) string {
		return fmt.Sprintf(`{"check_id":%q,"path":%q,"start":{"line":%d,"col":%d},"end":{"line":%d,"col":%d},"extra":{"message":"m","severity":"ERROR"}}`,
			checkID, path, sl, sc, el, ec)
	}
	for name, results := range map[string]string{
		"no rule identity":        row("", "a.go", 1, 1, 1, 2),
		"blank rule identity":     row("   ", "a.go", 1, 1, 1, 2),
		"prefix only identity":    row("opengrep:", "a.go", 1, 1, 1, 2),
		"no source path":          row("lycaon.go.a", "", 1, 1, 1, 2),
		"blank source path":       row("lycaon.go.a", "   ", 1, 1, 1, 2),
		"absent position":         `{"check_id":"lycaon.go.a","path":"a.go","extra":{"message":"m","severity":"ERROR"}}`,
		"zero start line":         row("lycaon.go.a", "a.go", 0, 1, 1, 2),
		"zero start column":       row("lycaon.go.a", "a.go", 1, 0, 1, 2),
		"zero end line":           row("lycaon.go.a", "a.go", 1, 1, 0, 2),
		"zero end column":         row("lycaon.go.a", "a.go", 1, 1, 1, 0),
		"negative line":           row("lycaon.go.a", "a.go", -4, -9, -1, -1),
		"end line before start":   row("lycaon.go.a", "a.go", 5, 1, 3, 1),
		"end column before start": row("lycaon.go.a", "a.go", 3, 9, 3, 2),
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			result, err := ParseOpengrepJSON(strings.NewReader(opengrepReport(results, "")))
			if err == nil {
				t.Fatalf("an unusable finding parsed as evidence: %+v", result.Findings)
			}
			if !strings.HasPrefix(err.Error(), "opengrep_json:") {
				t.Fatalf("parse failure lost its boundary label: %v", err)
			}
		})
	}
	result, err := ParseOpengrepJSON(strings.NewReader(opengrepReport(row("lycaon.go.a", "a.go", 3, 1, 4, 12), "")))
	testutil.FailErr(t, "parse a well-formed finding", err)
	if result.FindingsCount != 1 {
		t.Fatalf("well-formed finding was refused: %d kept", result.FindingsCount)
	}
}
