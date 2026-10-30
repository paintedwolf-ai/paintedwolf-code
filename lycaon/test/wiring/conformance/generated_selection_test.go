package conformance_test

import (
	"reflect"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/toolcontract"
	"github.com/lycaon/lycaon/internal/tools/native/command"
	"github.com/lycaon/lycaon/test/commandconformance"
	"github.com/lycaon/lycaon/test/referenceuserland"
)

func TestGeneratedSelection(t *testing.T) {
	t.Setenv("PW_CONFORMANCE_SEEDS", "")
	t.Setenv("PW_CONFORMANCE_PROGRAMS", "")
	seeds, count, err := generatedSelection()
	testutil.FailErr(t, "default selection", err)
	if !reflect.DeepEqual(seeds, commandconformance.GeneratedSeeds()) || count != commandconformance.GeneratedProgramsPerSeed {
		t.Fatalf("default %v %d", seeds, count)
	}
	t.Setenv("PW_CONFORMANCE_SEEDS", "0,18446744073709551615")
	t.Setenv("PW_CONFORMANCE_PROGRAMS", "64")
	seeds, count, err = generatedSelection()
	testutil.FailErr(t, "explicit selection", err)
	if len(seeds) != 2 || seeds[1] != ^uint64(0) || count != 64 {
		t.Fatalf("explicit %v %d", seeds, count)
	}
	for _, bad := range []string{"-1", "a", "18446744073709551616"} {
		t.Setenv("PW_CONFORMANCE_SEEDS", bad)
		if _, _, err := generatedSelection(); err == nil {
			t.Fatalf("accepted seed %q", bad)
		}
	}
	t.Setenv("PW_CONFORMANCE_SEEDS", "7")
	for _, bad := range []string{"0", "65", "a"} {
		t.Setenv("PW_CONFORMANCE_PROGRAMS", bad)
		if _, _, err := generatedSelection(); err == nil {
			t.Fatalf("accepted count %q", bad)
		}
	}
}

func TestGeneratedMismatchIdentity(t *testing.T) {
	refs := map[referenceuserland.ID]commandconformance.Snapshot{referenceuserland.GNU: {Status: 0, Stdout: commandconformance.Text("expected")}}
	obs := runtimeObservation{Outcome: command.ProgramOutcome{State: "completed", Stdout: "actual"}}
	first := generatedMismatch(obs, refs)
	obs.Outcome.Stdout = "shorter"
	if !reflect.DeepEqual(first, generatedMismatch(obs, refs)) {
		t.Fatal("stdout reduction changed mismatch identity")
	}
	obs.Outcome.Status = 1
	if reflect.DeepEqual(first, generatedMismatch(obs, refs)) {
		t.Fatal("status defect replaced stdout defect")
	}
	obs.Outcome.Status = 0
	obs.Outcome.Stdout = "expected"
	if generatedMismatch(obs, refs).Kind != "" {
		t.Fatal("matching observation remains a mismatch")
	}
	obs.Outcome.NativeGaps = 1
	if generatedMismatch(obs, refs).Kind != "native_gap" {
		t.Fatal("fallback treated as differential success")
	}
	obs.NativeGaps = []toolcontract.NativeGap{{Command: "sed", Code: "COMMAND_OPTION_UNSUPPORTED", Form: "-x"}}
	gap := generatedMismatch(obs, refs)
	obs.NativeGaps[0].Command = "grep"
	if reflect.DeepEqual(gap, generatedMismatch(obs, refs)) {
		t.Fatal("different native gap replaced original")
	}
	obs.Outcome.Refusal = &command.ProgramRefusal{Code: "COMMAND_OPTION_UNSUPPORTED", Details: map[string]any{"command": "sed", "word": "-x"}}
	refusal := generatedMismatch(obs, refs)
	obs.Outcome.Refusal.Details["command"] = "grep"
	if reflect.DeepEqual(refusal, generatedMismatch(obs, refs)) {
		t.Fatal("different refused command replaced original")
	}
	obs.TopLevelReject = "diagnostic only"
	if generatedMismatch(obs, refs).Kind != "top_level_rejection" {
		t.Fatal("top-level rejection lost")
	}
}
