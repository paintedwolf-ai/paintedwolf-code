package actioncard

import (
	"reflect"
	"testing"
	"time"

	"github.com/lycaon/lycaon/pkg/api"
)

func intPtr(v int) *int { return &v }

func kinds(facts []api.ActionFact) []api.ActionFactKind {
	out := make([]api.ActionFactKind, len(facts))
	for i, fact := range facts {
		out[i] = fact.Kind
	}
	return out
}

func fact(facts []api.ActionFact, kind api.ActionFactKind) (api.ActionFact, bool) {
	for _, f := range facts {
		if f.Kind == kind {
			return f, true
		}
	}
	return api.ActionFact{}, false
}

func commandCall(command string) api.ToolResult {
	return api.ToolResult{Tool: "command", ToolCallID: "call", ToolArgs: map[string]any{"command": command}, Content: "ok"}
}

func program(started time.Time, settled *time.Time, activities ...api.CommandActivity) api.CommandProgramReceipt {
	return api.CommandProgramReceipt{ID: "prog", SourceRef: "src", StartedAt: started, SettledAt: settled, Activities: activities}
}

// Each activity state settles into the card a person reads: status, result,
// timing for a whole-program activity, and the receipt behind it.
func TestActivityStatesDescribeTheirOutcome(t *testing.T) {
	start := time.Date(2026, 11, 15, 8, 0, 0, 0, time.UTC)
	settled := start.Add(4200 * time.Millisecond)
	cases := []struct {
		name      string
		activity  api.CommandActivity
		settledAt *time.Time
		status    string
		outcome   string
		exit      *int
		row       []api.ActionFactKind
		took      bool
	}{
		{"passed", api.CommandActivity{ID: "a", Label: "Run command", State: "completed", StatusCode: intPtr(0), ExecutionIDs: []string{"e"}}, &settled, statusDone, "passed", intPtr(0), []api.ActionFactKind{api.ActionFactResult, api.ActionFactTook}, true},
		{"exit nonzero", api.CommandActivity{ID: "a", State: "completed", StatusCode: intPtr(2), ExecutionIDs: []string{"e"}}, &settled, statusDone, "failed", intPtr(2), []api.ActionFactKind{api.ActionFactResult, api.ActionFactTook}, true},
		{"failed", api.CommandActivity{ID: "a", State: "failed", StatusCode: intPtr(1), ExecutionIDs: []string{"e"}}, &settled, statusError, "failed", intPtr(1), []api.ActionFactKind{api.ActionFactResult, api.ActionFactTook}, true},
		{"interrupted", api.CommandActivity{ID: "a", State: "interrupted", ExecutionIDs: []string{"e"}}, &settled, statusError, "interrupted", nil, []api.ActionFactKind{api.ActionFactResult, api.ActionFactTook}, true},
		{"running", api.CommandActivity{ID: "a", State: "running", ExecutionIDs: []string{"e"}}, nil, statusRunning, "", nil, []api.ActionFactKind{}, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			d := ForActivity(program(start, c.settledAt, c.activity), c.activity, commandCall("go test ./..."), nil)
			if d.Status != c.status || d.Ran.Text != "go test ./..." {
				t.Fatalf("status %q ran %q", d.Status, d.Ran.Text)
			}
			result, hasResult := fact(d.Facts, api.ActionFactResult)
			if hasResult != (c.outcome != "") || result.Outcome != c.outcome || !reflect.DeepEqual(result.ExitCode, c.exit) {
				t.Fatalf("result fact %+v", result)
			}
			took, hasTook := fact(d.Facts, api.ActionFactTook)
			if hasTook != c.took || (c.took && took.DurationMs != 4200) {
				t.Fatalf("took %+v", took)
			}
			if !reflect.DeepEqual(d.Row, c.row) {
				t.Fatalf("row %v", d.Row)
			}
			scope := []api.ActionReceiptScope{{ProgramID: "prog", ActivityID: "a"}}
			if d.Preview == nil || d.Preview.Kind != "output" || !reflect.DeepEqual(d.Preview.Receipts, scope) {
				t.Fatalf("preview %+v", d.Preview)
			}
			if last := d.Links[len(d.Links)-1]; last.Kind != linkReceipt || !reflect.DeepEqual(last.Receipts, scope) {
				t.Fatalf("links %+v", d.Links)
			}
		})
	}
}

func TestRefusedActivityNamesWhatStoppedIt(t *testing.T) {
	activity := api.CommandActivity{ID: "a", State: "refused"}
	p := program(time.Now(), nil, activity)
	p.StopCode = "COMMAND_WRITE_OUTSIDE_CATALOG"
	d := ForActivity(p, activity, commandCall("cat > a.go"), nil)
	if d.Status != statusError || !reflect.DeepEqual(kinds(d.Facts), []api.ActionFactKind{api.ActionFactResult, api.ActionFactStoppedBy}) {
		t.Fatalf("refused card %+v", d)
	}
	if stopped, _ := fact(d.Facts, api.ActionFactStoppedBy); stopped.Code != "COMMAND_WRITE_OUTSIDE_CATALOG" || stopped.By != "host_stop" {
		t.Fatalf("stopped by %+v", stopped)
	}
	if d.Preview != nil {
		t.Fatalf("an activity with no executions previewed output: %+v", d.Preview)
	}
}

func TestBackgroundActivityOpensItsProcess(t *testing.T) {
	activity := api.CommandActivity{ID: "a", Label: "Run process", State: "running", JobHandle: "job-1", ExecutionIDs: []string{"e"}}
	d := ForActivity(program(time.Now(), nil, activity), activity, commandCall("npm run dev"), nil)
	if d.Label != "Run process" || d.Opener == nil || d.Opener.Kind != "running" || d.Opener.ProcessHandle != "job-1" {
		t.Fatalf("background card %+v", d)
	}
	if process, _ := fact(d.Facts, api.ActionFactProcess); process.State != "running" {
		t.Fatalf("process fact %+v", process)
	}
	if output := d.Links[1]; output.Kind != linkOutput || output.ProcessHandle != "job-1" {
		t.Fatalf("output link %+v", output)
	}
}

func TestRepeatedExecutionsBecomeSteps(t *testing.T) {
	activity := api.CommandActivity{ID: "a", State: "completed", StatusCode: intPtr(0), ExecutionCount: 4, FailedCount: 1, ExecutionIDs: []string{"e"}}
	d := ForActivity(program(time.Now(), nil, activity), activity, commandCall("go test ./..."), nil)
	steps, ok := fact(d.Facts, api.ActionFactSteps)
	if !ok || steps.Count != 4 || steps.Failed != 1 || steps.Unit != "executions" {
		t.Fatalf("steps %+v", steps)
	}
}

// An activity of a compound command shows only its own statements, unless
// redaction marks have shifted the source offsets.
func TestCompoundCommandShowsEachActivitysStatements(t *testing.T) {
	source := "cat > a.go <<'EOF'\npackage a\nEOF\ngo test ./..."
	write := api.CommandActivity{ID: "w", State: "completed", Subject: "> a.go", Spans: []api.CommandActivitySpan{{SourceRef: "src", StartByte: 0, EndByte: 32}}}
	test := api.CommandActivity{ID: "t", State: "completed", Subject: "go test", Spans: []api.CommandActivitySpan{{SourceRef: "src", StartByte: 33, EndByte: len(source)}}}
	p := program(time.Now(), nil, write, test)
	if got := ForActivity(p, write, commandCall(source), nil).Ran.Text; got != "cat > a.go <<'EOF'\npackage a\nEOF" {
		t.Fatalf("write statements %q", got)
	}
	if got := ForActivity(p, test, commandCall(source), nil).Ran.Text; got != "go test ./..." {
		t.Fatalf("test statements %q", got)
	}
	marks := []api.RedactedSpan{{Field: "tool_result.tool_args.command", Start: 3, Length: 10}}
	if got := ForActivity(p, test, commandCall(source), marks).Ran.Text; got != "go test" {
		t.Fatalf("redacted source was sliced: %q", got)
	}
}

func TestWholeCommandCarriesItsRedactionMarks(t *testing.T) {
	activity := api.CommandActivity{ID: "a", State: "completed"}
	marks := []api.RedactedSpan{{Field: "tool_result.tool_args.command", Start: 5, Length: 8, Kind: api.RedactionKind("secret")}}
	d := ForActivity(program(time.Now(), nil, activity), activity, commandCall("curl [secret] https://x"), marks)
	if d.Ran.Redaction == nil || len(d.Ran.Redaction.Spans) != 1 || d.Ran.Redaction.Spans[0].Field != RanTextField || d.Ran.Redaction.Spans[0].Start != 5 {
		t.Fatalf("ran redaction %+v", d.Ran.Redaction)
	}
}

func TestSliceSourceMergesAndDropsRepeats(t *testing.T) {
	source := "echo a\necho a\necho b"
	spans := []api.CommandActivitySpan{
		{SourceRef: "src", StartByte: 14, EndByte: 20},
		{SourceRef: "src", StartByte: 0, EndByte: 4},
		{SourceRef: "src", StartByte: 2, EndByte: 6},
		{SourceRef: "src", StartByte: 7, EndByte: 13},
		{SourceRef: "other", StartByte: 0, EndByte: 6},
		{SourceRef: "src", StartByte: 5, EndByte: 99},
	}
	if got := sliceSource(source, spans, "src"); got != "echo a\necho b" {
		t.Fatalf("slice %q", got)
	}
}

// Form tools run as programs, so their activities show the program the host wrote.
func TestFormToolActivityShowsTheHostsProgram(t *testing.T) {
	activity := api.CommandActivity{ID: "a", State: "completed"}
	result := api.ToolResult{Tool: "survey_repo", ToolArgs: map[string]any{"path": "src", "bundle": "api_routes"}}
	if got := ForActivity(program(time.Now(), nil, activity), activity, result, nil).Ran.Text; got != "repo-map --path src --bundle api_routes" {
		t.Fatalf("form program %q", got)
	}
}

func TestCommandWithoutAProgramStatesItsRefusal(t *testing.T) {
	result := commandCall("rm -rf /")
	result.Outcome = api.ToolResultOutcomeRejected
	result.Codes = []string{"COMMAND_DESTRUCTIVE"}
	d := ForResult(result, nil)
	if d == nil || d.Label != "Run command" || d.Status != statusError || d.Ran.Text != "rm -rf /" {
		t.Fatalf("refused call %+v", d)
	}
	if stopped, _ := fact(d.Facts, api.ActionFactStoppedBy); stopped.Code != "COMMAND_DESTRUCTIVE" {
		t.Fatalf("stopped by %+v", stopped)
	}
	result.CommandProgram = &api.CommandProgramReceipt{ID: "prog", Activities: []api.CommandActivity{{ID: "a"}}}
	if ForResult(result, nil) != nil {
		t.Fatal("a program's result was described instead of its activities")
	}
}

// A program that recorded no activities is described by the program itself.
func TestProgramWithoutActivitiesUsesItsOwnState(t *testing.T) {
	settled := time.Unix(105, 0)
	result := commandCall("exit 3")
	result.CommandProgram = &api.CommandProgramReceipt{ID: "prog", State: "completed", StatusCode: intPtr(3), StartedAt: time.Unix(100, 0), SettledAt: &settled}
	d := ForResult(result, nil)
	if d == nil || d.Status != statusDone {
		t.Fatalf("program card %+v", d)
	}
	if got, _ := fact(d.Facts, api.ActionFactResult); got.Outcome != "failed" || got.ExitCode == nil || *got.ExitCode != 3 {
		t.Fatalf("result %+v", got)
	}
	if took, _ := fact(d.Facts, api.ActionFactTook); took.DurationMs != 5000 {
		t.Fatalf("took %+v", took)
	}
	if last := d.Links[len(d.Links)-1]; last.Kind != linkReceipt || last.Receipts[0].ProgramID != "prog" {
		t.Fatalf("links %+v", d.Links)
	}
}
