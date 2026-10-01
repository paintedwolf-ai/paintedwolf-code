package output

import (
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
)

func TestOpengrepRejectsAmbiguousReportEnvelopes(t *testing.T) {
	t.Parallel()
	for name, raw := range map[string]string{
		"no results":       `{}`,
		"metadata only":    `{"version":"1.29.0","paths":{"scanned":["a.go"]}}`,
		"repeated results": `{"results":[` + oneOpengrepFinding + `],"results":[]}`,
		"repeated errors":  `{"results":[` + oneOpengrepFinding + `],"errors":[{}],"errors":[]}`,
		"repeated paths":   `{"results":[],"paths":{"scanned":["a.go"]},"paths":{"scanned":[]}}`,
		"repeated unknown": `{"results":[],"version":"1","version":"2"}`,
		"escaped key":      `{"results":[],"res\u0075lts":[]}`,
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			assertOpengrepReportRejected(t, raw)
		})
	}
}

func TestOpengrepRejectsUnusableFindingIdentityAndSpans(t *testing.T) {
	t.Parallel()
	for name, replacement := range map[string][2]string{
		"empty rule":       {`"check_id":"lycaon.go.a"`, `"check_id":""`},
		"blank rule":       {`"check_id":"lycaon.go.a"`, `"check_id":"  "`},
		"empty rule body":  {`"check_id":"lycaon.go.a"`, `"check_id":"opengrep:"`},
		"empty path":       {`"path":"a.go"`, `"path":""`},
		"zero start line":  {`"start":{"line":1,"col":1}`, `"start":{"line":0,"col":1}`},
		"negative column":  {`"start":{"line":1,"col":1}`, `"start":{"line":1,"col":-1}`},
		"missing end":      {`"end":{"line":1,"col":2}`, `"end":{}`},
		"reversed lines":   {`"start":{"line":1,"col":1}`, `"start":{"line":2,"col":1}`},
		"reversed columns": {`"start":{"line":1,"col":1}`, `"start":{"line":1,"col":3}`},
		"zero end column":  {`"end":{"line":1,"col":2}`, `"end":{"line":1,"col":0}`},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			row := strings.Replace(oneOpengrepFinding, replacement[0], replacement[1], 1)
			assertOpengrepReportRejected(t, opengrepReport(oneOpengrepFinding+","+row, ""))
		})
	}
}

func TestOpengrepAcceptsValidEmptyAndMultilineReports(t *testing.T) {
	t.Parallel()
	for name, raw := range map[string]string{
		"empty findings": `{"results":[],"errors":[]}`,
		"unknown nested": `{"results":[],"metadata":{"results":[],"nested":[{"errors":[]}]}}`,
		"multiline": opengrepReport(strings.Replace(oneOpengrepFinding,
			`"end":{"line":1,"col":2}`, `"end":{"line":2,"col":1}`, 1), ""),
		"empty span": opengrepReport(strings.Replace(oneOpengrepFinding,
			`"end":{"line":1,"col":2}`, `"end":{"line":1,"col":1}`, 1), ""),
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			_, err := ParseOpengrepJSON(strings.NewReader(raw))
			testutil.FailErr(t, "parse valid report", err)
		})
	}
}

func assertOpengrepReportRejected(t *testing.T, raw string) {
	t.Helper()
	result, err := ParseOpengrepJSON(strings.NewReader(raw))
	if result != nil || err == nil || !strings.HasPrefix(err.Error(), "opengrep_json:") {
		t.Fatalf("invalid streaming report returned result=%v, err=%v", result, err)
	}
	result, err = newOpengrepJSONParser().Parse([]byte(raw))
	if result != nil || err == nil || !strings.HasPrefix(err.Error(), "opengrep_json:") {
		t.Fatalf("invalid catalog report returned result=%v, err=%v", result, err)
	}
}
