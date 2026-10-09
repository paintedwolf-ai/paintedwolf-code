package worker

import (
	"reflect"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestSharedContextPreservesContractAndHasIndependentByteBudget(t *testing.T) {
	contract := "type Item = {\n  value: string;\n}\n\nrender(item);"
	args := map[string]any{"brief": map[string]any{"goal": "integrate", "done_when": []string{"shared interface works"}}, "shared_context": contract}
	charter, err := parseTaskCharter(args)
	testutil.FailErr(t, "parse shared contract", err)
	if charter.SharedContext != contract || !strings.Contains(formatTaskCharter(charter), contract) {
		t.Fatal("shared contract formatting changed")
	}
	args["shared_context"] = strings.Repeat("a", 8192)
	_, err = parseTaskCharter(args)
	testutil.FailErr(t, "accept independent context allowance", err)
	args["shared_context"] = strings.Repeat("界", 2731)
	if _, err := parseTaskCharter(args); err == nil {
		t.Fatal("accepted context exceeding byte budget")
	}
}

func TestPlannedCharterKeepsHostAssignment(t *testing.T) {
	planned := api.WorkerTaskCharter{Goal: "Inspect the full boundary", DoneWhen: []string{"Trace every entry point"}, SharedContext: "Local authenticated system"}
	got, err := plannedTaskCharter(&planned, map[string]any{"brief": map[string]any{"goal": "Skip the boundary", "done_when": []any{"Read one file"}}})
	testutil.FailErr(t, "bind planned charter", err)
	if got.Goal != planned.Goal || !reflect.DeepEqual(got.DoneWhen, planned.DoneWhen) || !strings.Contains(got.SharedContext, "Coordinator supplement") {
		t.Fatalf("planned assignment changed: %+v", got)
	}
	direct, err := plannedTaskCharter(&planned, nil)
	testutil.FailErr(t, "dispatch with work id only", err)
	if !reflect.DeepEqual(direct, planned) {
		t.Fatalf("unexpected charter: %+v", direct)
	}
}
