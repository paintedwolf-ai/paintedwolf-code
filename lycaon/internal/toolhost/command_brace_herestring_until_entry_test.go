//go:build unix

package toolhost

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/toolcontract"
	"github.com/lycaon/lycaon/internal/toolrejection"
)

func TestCommandEntryBraceExpansionReachesTheFilesystemOwner(t *testing.T) {
	f := newPathsFixture(t)
	out, err := f.Call(t, t.Context(), `echo {a,b}{1,2}; echo {01..03}; echo "{a,b}"; mkdir -p @scratch/x/{a,b}; ls @scratch/x`)
	testutil.FailErr(t, "run brace program", err)
	got := decodeProgramOutcome(t, out)
	if got.Status != 0 || got.Stdout != "a1 a2 b1 b2\n01 02 03\n{a,b}\na\nb\n" || got.Refusal != nil {
		t.Fatalf("brace outcome: %+v", got)
	}
	for _, name := range []string{"a", "b"} {
		info, statErr := os.Stat(filepath.Join(f.scratch, "x", name))
		if statErr != nil || !info.IsDir() {
			t.Fatalf("mkdir did not create %s: %v", name, statErr)
		}
	}
	if executions := f.Executions(t); executions["mkdir"] != "completed;" {
		t.Fatalf("mkdir did not settle through its owner: %v", executions)
	}
}

func TestCommandEntryBraceOverTheFieldBoundRefusesBeforeDispatch(t *testing.T) {
	f := newPathsFixture(t)
	source := "mkdir ./never; echo x{1..16385}"
	_, err := f.Call(t, t.Context(), source)
	rejected := toolrejection.AsToolReject(err)
	if rejected == nil || rejected.Code != "COMMAND_LIMIT_EXCEEDED" || rejected.Data["limit_id"] != "expanded_fields" {
		t.Fatalf("over-bound brace: %v", err)
	}
	start, _ := rejected.Data["source_start_byte"].(int)
	end, _ := rejected.Data["source_end_byte"].(int)
	if source[start:end] != "x{1..16385}" {
		t.Fatalf("refusal spans %q: %v", source[start:end], rejected.Data)
	}
	if _, statErr := os.Stat(filepath.Join(f.Dir, "never")); !os.IsNotExist(statErr) {
		t.Fatal("an earlier statement ran before brace admission")
	}
	if programs := f.Programs(t); len(programs) != 0 {
		t.Fatalf("refused program recorded %v", programs)
	}
}

func TestCommandEntryHereStringFeedsStdin(t *testing.T) {
	f := newPathsFixture(t)
	out, err := f.Call(t, t.Context(), `wc -c <<< "abc"; json='{"a":1}'; jq -c .a <<< "$json"; grep -c x <<< "$(printf 'x\nyx\nz')"`)
	testutil.FailErr(t, "run here-string program", err)
	got := decodeProgramOutcome(t, out)
	if got.Status != 0 || got.Stdout != "4\n1\n2\n" {
		t.Fatalf("here-string outcome: %+v", got)
	}
}

func TestCommandEntryUntilExitsWhenAnotherJobCreatesTheFile(t *testing.T) {
	f := newPathsFixture(t)
	// The loop polls twice with the file absent before it starts the job that
	// creates it; it then exits on the job's effect, never on elapsed time.
	program := `n=0; until [ -f done ]; do n=$((n+1)); if [ "$n" -eq 2 ]; then touch done & fi; sleep 0.1; done; wait; echo "polls $n"`
	out, err := f.Call(t, t.Context(), program)
	testutil.FailErr(t, "run readiness loop", err)
	got := decodeProgramOutcome(t, out)
	polls, parseErr := strconv.Atoi(strings.TrimSpace(strings.TrimPrefix(got.Stdout, "polls ")))
	if got.Status != 0 || got.Refusal != nil || parseErr != nil || polls < 2 {
		t.Fatalf("readiness outcome: %+v", got)
	}
	if _, statErr := os.Stat(filepath.Join(f.Dir, "done")); statErr != nil {
		t.Fatalf("the job's file is missing: %v", statErr)
	}
}

func TestCommandEntryUntilRespectsTheLoopBudget(t *testing.T) {
	f := newPathsFixture(t)
	out, err := f.Call(t, t.Context(), "until false; do :; done; echo unreachable")
	testutil.FailErr(t, "run unbounded readiness loop", err)
	got := decodeProgramOutcome(t, out)
	if got.Refusal == nil || got.Refusal.Code != "COMMAND_LIMIT_EXCEEDED" || got.Refusal.Details["limit_id"] != "loop_iterations" || strings.Contains(got.Stdout, "unreachable") {
		t.Fatalf("loop budget outcome: %+v", got)
	}
}

func TestCommandEntryAssigningDefaultAndPositionalRefusal(t *testing.T) {
	f := newPathsFixture(t)
	out, err := f.Call(t, t.Context(), `: ${PORT:=8080}; echo $PORT; echo ${PORT:=9090} ${HOST=localhost} $HOST`)
	testutil.FailErr(t, "run assigning defaults", err)
	if got := decodeProgramOutcome(t, out); got.Status != 0 || got.Stdout != "8080\n8080 localhost localhost\n" {
		t.Fatalf("assigning default outcome: %+v", got)
	}
	source := "mkdir ./never; : ${1:=x}"
	_, err = f.Call(t, t.Context(), source)
	rejected := toolrejection.AsToolReject(err)
	if rejected == nil {
		t.Fatalf("positional assignment: %v", err)
	}
	start, _ := rejected.Data["source_start_byte"].(int)
	if rejected.Code != "COMMAND_SYNTAX_UNSUPPORTED" || start != strings.Index(source, "${1") {
		t.Fatalf("positional assignment: %v", err)
	}
	if _, statErr := os.Stat(filepath.Join(f.Dir, "never")); !os.IsNotExist(statErr) {
		t.Fatal("an earlier statement ran before parameter admission")
	}
}

// TestCatalogGoldenProgramsForBracesHereStringsUntilAndAssignments runs the
// catalog's golden programs for these forms through the composed entry.
func TestCatalogGoldenProgramsForBracesHereStringsUntilAndAssignments(t *testing.T) {
	catalog, err := toolcontract.LanguageV1()
	testutil.FailErr(t, "catalog", err)
	var contract struct {
		Golden []struct {
			ID       string `json:"id"`
			Program  string `json:"program"`
			Expected struct {
				Stdout     string  `json:"stdout"`
				Status     int     `json:"status"`
				Diagnostic *string `json:"diagnostic"`
				Span       *struct {
					Start int `json:"start_byte"`
					End   int `json:"end_byte"`
				} `json:"source_span"`
			} `json:"expected"`
		} `json:"golden_programs"`
	}
	testutil.FailErr(t, "golden contract", json.Unmarshal(catalog.Contracts["golden"], &contract))
	ran := map[string]bool{}
	for _, golden := range contract.Golden {
		switch golden.ID {
		case "brace-expansion", "brace-field-bound", "brace-zero-step", "brace-mixed-range", "here-string", "until-loop", "parameter-assign-default", "parameter-assign-positional":
		default:
			continue
		}
		ran[golden.ID] = true
		t.Run(golden.ID, func(t *testing.T) {
			f := newPathsFixture(t)
			out, err := f.Call(t, t.Context(), golden.Program)
			if golden.Expected.Diagnostic != nil {
				rejected := toolrejection.AsToolReject(err)
				if rejected == nil || rejected.Code != *golden.Expected.Diagnostic || golden.Expected.Span == nil ||
					rejected.Data["source_start_byte"] != golden.Expected.Span.Start || rejected.Data["source_end_byte"] != golden.Expected.Span.End {
					t.Fatalf("refusal %v, want %s at %+v", err, *golden.Expected.Diagnostic, golden.Expected.Span)
				}
				if _, statErr := os.Stat(filepath.Join(f.Dir, "never")); !os.IsNotExist(statErr) {
					t.Fatal("a refused golden program wrote before admission")
				}
				return
			}
			testutil.FailErr(t, "run golden program", err)
			if got := decodeProgramOutcome(t, out); got.Status != golden.Expected.Status || got.Stdout != golden.Expected.Stdout {
				t.Fatalf("golden outcome: %+v", got)
			}
		})
	}
	if len(ran) != 8 {
		t.Fatalf("golden programs exercised: %v", ran)
	}
}
