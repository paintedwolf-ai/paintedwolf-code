package output

import (
	"encoding/json"
	"strings"
	"testing"

	scanfindings "github.com/lycaon/lycaon/internal/scan/findings"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

func traceLocationJSON(path string, line int) string {
	raw, _ := json.Marshal(opengrepTraceLocation{Path: path, Start: opengrepPosition{Line: line, Col: 1}, End: opengrepPosition{Line: line, Col: 5}})
	return string(raw)
}

func TestOpengrepNestedEvidence(t *testing.T) {
	loc := func(line int) string { return traceLocationJSON("/snapshot/app.py", line) }
	leaf := `["CliLoc",[` + loc(2) + `,"secret source text"]]`
	call := `["CliCall",[[` + loc(7) + `,"fetch()"],[{"location":` + loc(3) + `,"content":"secret variable"}],` + leaf + `]]`
	raw := `{"results":[{"check_id":"lycaon.python.test","path":"/snapshot/app.py","start":{"line":9,"col":1},"end":{"line":9,"col":5},"extra":{"message":"Unsafe flow","severity":"ERROR","dataflow_trace":{"taint_source":` + call + `,"intermediate_vars":[{"location":` + loc(8) + `,"content":"secret"}],"taint_sink":["CliLoc",[` + loc(9) + `,"sink(value)"]]}}}],"errors":[]}`
	result, err := ParseOpengrepJSON(strings.NewReader(raw))
	testutil.FailErr(t, "parse nested trace", err)
	if len(result.Findings) != 1 {
		t.Fatalf("findings = %d", len(result.Findings))
	}
	finding := &result.Findings[0]
	flow := finding.Dataflow
	if flow == nil || flow.Source == nil || flow.Source.Callee == nil || flow.Sink == nil {
		t.Fatalf("incomplete trace: %#v", flow)
	}
	if flow.Source.Location.StartLine != 7 || flow.Source.Callee.Location.StartLine != 2 || flow.Source.Intermediates[0].StartLine != 3 || flow.Intermediates[0].StartLine != 8 || flow.Sink.Location.StartLine != 9 {
		t.Fatalf("call structure changed: %#v", flow)
	}
	NormalizeResultPaths(result, "/snapshot")
	scanfindings.VisitFindingLocations(finding, func(location *api.SecurityFindingLocation) {
		if location.URI != "app.py" {
			t.Errorf("unmapped location: %#v", location)
		}
	})
	encoded, err := json.Marshal(finding)
	testutil.FailErr(t, "serialize evidence", err)
	if strings.Contains(string(encoded), "secret") {
		t.Fatal("source contents escaped into finding")
	}
	var persisted api.SecurityFinding
	testutil.FailErr(t, "read persisted evidence", json.Unmarshal(encoded, &persisted))
	if persisted.Dataflow.Source.Callee.Location.StartLine != 2 {
		t.Fatal("nested evidence lost in serialization")
	}
	flows := sarifDataflow(persisted.Dataflow)
	if len(flows) != 1 || len(flows[0].ThreadFlows) != 1 {
		t.Fatal("missing SARIF flow")
	}
	steps := flows[0].ThreadFlows[0].Locations
	wantLines := []int{2, 3, 7, 8, 9}
	if len(steps) != len(wantLines) {
		t.Fatalf("SARIF steps = %d", len(steps))
	}
	for i, line := range wantLines {
		if *steps[i].Location.PhysicalLocation.Region.StartLine != line {
			t.Errorf("SARIF step %d order changed", i)
		}
	}
	if *steps[0].NestingLevel != 1 || *steps[2].NestingLevel != 0 {
		t.Fatal("SARIF call nesting lost")
	}
}

func TestOpengrepRejectsMalformedEvidence(t *testing.T) {
	valid := `["CliLoc",[` + traceLocationJSON("app.py", 2) + `,"text"]]`
	for _, raw := range []string{
		`[]`, `["Unknown",[]]`, `["CliLoc",[]]`, `["CliLoc",[{},"text"]]`,
		`["CliCall",[[` + traceLocationJSON("app.py", 2) + `,"text"],[],null]]`,
	} {
		if _, err := decodeOpengrepCallTrace(json.RawMessage(raw), 0); err == nil {
			t.Errorf("accepted %s", raw)
		}
	}
	if _, err := decodeOpengrepCallTrace(json.RawMessage(valid), maxOpengrepCallDepth); err == nil {
		t.Fatal("accepted excessive trace nesting")
	}
	flow, err := (&opengrepDataflow{Sink: json.RawMessage(valid)}).evidence()
	testutil.FailErr(t, "parse partial evidence", err)
	if flow.Source != nil || flow.Sink == nil {
		t.Fatal("partial trace fabricated or dropped")
	}
}
