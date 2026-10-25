package query

import (
	"bytes"
	"io"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/commandprogram"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/toolcontract"
	"github.com/lycaon/lycaon/internal/tools/native"
	nativefixture "github.com/lycaon/lycaon/internal/tools/native/internal/testfixture"
	"github.com/lycaon/lycaon/internal/tools/native/textutil"
)

// awkRun dispatches one awk invocation through P14's bounded GoAWK owner,
// the single field and record engine queries share.
func (h *harness) awkRun(stdin string, words ...string) (string, toolcontract.ProducerResult, string) {
	h.t.Helper()
	owners, err := textutil.NewOwners(native.ExecutionRevisions{Boundary: nativefixture.Boundary(h.t)}, h.pins)
	testutil.FailErr(h.t, "text owners", err)
	limits, err := toolcontract.CatalogProgramMemoryLimits()
	testutil.FailErr(h.t, "memory limits", err)
	memory, err := toolcontract.NewProgramMemory(limits)
	testutil.FailErr(h.t, "memory", err)
	argv := make(commandprogram.Fields, len(words))
	for i, word := range words {
		argv[i] = commandprogram.Field{Value: word}
	}
	request := commandprogram.OperationRequest{OperationID: "awk", Argv: argv, Invocation: toolcontract.OperationInvocation{Kind: toolcontract.ProgramChildInvocation,
		Identity: toolcontract.ExecutionIdentity{ProgramID: "program", ExecutionID: "awk-child"}, Scope: toolcontract.OperationScope{Kind: toolcontract.ProjectOperationScope, ProjectID: "p1"}}}
	var stdout, stderr bytes.Buffer
	result, err := owners.Builtins(textutil.Ports{ToolContext: h.tc, Memory: memory})["awk"].Invoke(h.t.Context(), request, commandprogram.Streams{Input: io.NopCloser(strings.NewReader(stdin)), Output: &stdout, Error: &stderr})
	testutil.FailErr(h.t, "awk", err)
	return stdout.String(), result, stderr.String()
}

// TestFieldAndRecordQueriesShareTheAwkOwner turns structured values into
// records with jq and queries their fields with awk, and queries a pinned
// record file directly: one value engine, one record engine.
func TestFieldAndRecordQueriesShareTheAwkOwner(t *testing.T) {
	h := newHarness(t, map[string]string{
		"services.json": `{"services":[{"name":"api","port":8080},{"name":"db","port":5432},{"name":"web","port":9090}]}`,
		"services.tsv":  "api\t8080\ndb\t5432\nweb\t9090\n",
	})
	records := h.exec("", "jq", "-r", `.services[]|[.name,(.port|tostring)]|@tsv`, "services.json")
	records.expect(t, "jq records", "api\t8080\ndb\t5432\nweb\t9090\n", 0)
	out, result, stderr := h.awkRun(records.stdout, "awk", "-F", "\t", "$2 > 8000 { print $1 }")
	if out != "api\nweb\n" || result.Status != 0 || stderr != "" {
		t.Fatalf("awk over jq records = %q %+v %q", out, result, stderr)
	}
	out, result, _ = h.awkRun("", "awk", "-F", "\t", "{ sum += $2 } END { print NR, sum }", "services.tsv")
	if out != "3 22602\n" || result.Status != 0 {
		t.Fatalf("awk over a pinned record file = %q %+v", out, result)
	}
	if pins := h.pins.pins["awk-child"]; len(pins) != 1 || pins[0].Path != "services.tsv" {
		t.Fatalf("record file pins = %+v", pins)
	}
	csv := h.exec("", "jq", "-r", `.services[]|[.name,.port]|@csv`, "services.json")
	csv.expect(t, "jq csv", "\"api\",8080\n\"db\",5432\n\"web\",9090\n", 0)
	out, _, _ = h.awkRun(csv.stdout, "awk", "-F", ",", "NR == 2 { print $2 }")
	if out != "5432\n" {
		t.Fatalf("awk over csv = %q", out)
	}
}
