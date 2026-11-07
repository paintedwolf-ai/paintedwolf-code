package invocation

import (
	"context"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/toolcontract"
	"github.com/lycaon/lycaon/pkg/api"
)

func activityFixture(id, parent, operation, command string, start int) activityInput {
	zero := 0
	return activityInput{operation: operation, pipeline: parent != "root", arguments: map[string]any{}, receipt: api.CommandExecutionReceipt{ID: id, NodeID: id, ParentExecutionID: parent, NativeCommand: command, Command: command, State: "completed", StatusCode: &zero, Lifecycle: "read_only", EffectClasses: []string{"read_only"}}, span: api.CommandActivitySpan{SourceRef: "source", StartByte: start, EndByte: start + 3}}
}

func activityGroups(inputs ...activityInput) []activityGroup {
	var groups []activityGroup
	for i, input := range inputs {
		groups = append(groups, activityGroup{input: input, key: activityKey{node: input.receipt.ID}, parent: activityKey{node: input.receipt.ParentExecutionID}, ordinal: int64(i + 1), counts: activityCounts{total: 1}})
	}
	return groups
}

func TestActivityStructureFoldsPipelineRegardlessOfStartOrder(t *testing.T) {
	pipe := activityFixture("pipe", "root", "program |", "", 0)
	head := activityFixture("head", "pipe", "read", "head", 20)
	process := activityFixture("process", "pipe", "command", "external", 0)
	process.arguments["command"] = "scc src"
	groups := activityGroups(pipe, head, process)
	before := composeActivityGroups(groups[:2], false)
	after := composeActivityGroups(groups, false)
	if len(before) != 0 || len(after) != 1 || after[0].ID != "process" || after[0].ExecutionCount != 2 || after[0].Subject != "scc src" || after[0].Phase != "execute" {
		t.Fatalf("pipeline activities: before=%+v after=%+v", before, after)
	}
	if got := composeActivityGroups(groups, true); got[0].ID != after[0].ID {
		t.Fatal("settlement moved the row")
	}
}

func TestActivityStructureCollapsesFindChildrenAndPreservesConsequences(t *testing.T) {
	find := activityFixture("find", "root", "find", "find", 0)
	dirname := activityFixture("dirname", "find", "dirname", "dirname", 0)
	write := activityFixture("write", "find", "write", "write", 0)
	write.receipt.EffectClasses = []string{"mutation"}
	groups := activityGroups(find, dirname, write)
	groups[1].counts.total = 10705
	activities := composeActivityGroups(groups, true)
	if len(activities) != 2 || activities[0].ExecutionCount != 10706 || len(activities[0].ExecutionIDs) != 2 || activities[0].Phase != "inspect" || activities[1].Label != "Change files" || activities[1].Phase != "mutate" {
		t.Fatalf("effects and repetition: %+v", activities)
	}
}

func TestActivityCountsRollSupportingFailuresIntoTheRow(t *testing.T) {
	pipe := activityFixture("pipe", "root", "program |", "", 0)
	process := activityFixture("process", "pipe", "command", "external", 0)
	sort := activityFixture("sort", "pipe", "sort", "sort -rn", 20)
	groups := activityGroups(pipe, process, sort)
	groups[1].counts.total = 318
	groups[2].counts.failed = 1
	groups[2].input.receipt.State = "failed"
	activities := composeActivityGroups(groups, true)
	if len(activities) != 1 || activities[0].State != "failed" || activities[0].FailedCount != 1 || activities[0].Repetitions != 318 || !strings.Contains(activities[0].Outcome, "reported failure") {
		t.Fatalf("outcomes: %+v", activities)
	}
	if activities[0].Steps[0].Outcome != "Exited 0" || activities[0].Steps[1].State != "failed" {
		t.Fatalf("step outcomes: %+v", activities[0].Steps)
	}
	groups[1].counts.running = 1
	if got := composeActivityGroups(groups, false)[0]; got.State != "running" || got.ID != activities[0].ID {
		t.Fatalf("live repetition: %+v", got)
	}
}

func TestRecordingPhaseFoldsAndRollsRefusal(t *testing.T) {
	stage := activityFixture("stage", "root", "git_add", "git add", 0)
	commit := activityFixture("commit", "root", "git_commit", "git commit", 5)
	cd := activityFixture("cd", "root", "cd", "cd", 10)
	groups := activityGroups(stage, commit, cd)
	groups[1].counts.refused = 1
	groups[1].input.receipt.State = "refused"
	groups[2].counts.failed = 1
	groups[2].input.receipt.State = "failed"
	values := composeActivityGroups(groups, true)
	if len(values) != 2 || values[0].Group != "Recording changes" || values[0].State != "refused" || values[0].FailedCount != 1 || len(values[0].Steps) != 2 {
		t.Fatalf("recording fold: %+v", values)
	}
	if values[0].Steps[1].State != "refused" || values[0].Steps[1].Outcome != "Refused" {
		t.Fatalf("refusal facts: %+v", values[0].Steps[1])
	}
	if values[1].State != "failed" {
		t.Fatalf("standalone failure: %+v", values[1])
	}
	live := composeActivityGroups(groups[:2], false)
	if len(live) != 1 || live[0].ID != values[0].ID {
		t.Fatalf("settlement moved the row: %+v", live)
	}
}

func TestActivitiesUsePathsAndCatalogFacts(t *testing.T) {
	read := activityFixture("read", "root", "sed", "sed", 0)
	read.family = "sed"
	read.arguments["path_targets"] = map[string]any{"read": []any{"file.rs"}}
	external := activityFixture("external", "root", "command", "external", 10)
	external.arguments["command"] = "custom-helper claim-tests-passed"
	values := composeActivityGroups(activityGroups(read, external), true)
	if len(values) != 2 || values[0].Label != "Read source" || values[0].Subject != "file.rs" || values[0].Phase != "inspect" || values[1].Label != "Run process" || values[1].Phase != "execute" {
		t.Fatalf("classification: %+v", values)
	}
}

func TestActivitiesRetainControlOnlyAndUnknownOperations(t *testing.T) {
	control := activityFixture("cd", "root", "cd", "cd", 0)
	if values := composeActivityGroups(activityGroups(control), true); len(values) != 1 {
		t.Fatal("control-only execution disappeared")
	}
	if values := composeActivityGroups(activityGroups(control), false); len(values) != 0 {
		t.Fatal("context became provisional work")
	}
	unknown := activityFixture("unknown", "root", "extension", "extension", 0)
	unknown.receipt.Lifecycle = "effect_attempt"
	unknown.receipt.EffectClasses = nil
	if got := composeActivityGroups(activityGroups(unknown), true)[0]; got.Label != "Run command" {
		t.Fatalf("unknown operation: %+v", got)
	}
	for _, state := range []string{"prepared", "waiting", "refused", "failed", "interrupted", "unknown"} {
		unknown.receipt.State = state
		if _, outcome := activityOutcome(unknown.receipt); outcome == "" {
			t.Fatalf("missing outcome for %s", state)
		}
	}
}

func TestActivityInputBoundsAndScreenedValue(t *testing.T) {
	budget := &activityReadBudget{}
	raw := map[string]any{"command": "custom-helper", "cwd": strings.Repeat("x", 600), "path_targets": map[string]any{"read": []any{"safe/[REDACTED]"}}}
	reviewed, release, err := toolcontract.NewReviewedOperationInputs(t.Context(), budget, raw)
	testutil.FailErr(t, "encode screened fixture", err)
	value, err := activityArguments(t.Context(), []byte(reviewed.CanonicalArguments()), reviewed.ArgumentsDigest())
	testutil.FailErr(t, "read screened presentation", err)
	release()
	if len([]rune(stringArgument(value, "cwd"))) != 257 || activityPaths(value, "read")[0] != "safe/[REDACTED]" {
		t.Fatalf("bounded projection: %#v", value)
	}
	if budget.used != 0 {
		t.Fatal("input inspection leaked admission")
	}
	if _, err := activityArguments(t.Context(), []byte("[]"), "wrong"); err == nil {
		t.Fatal("accepted malformed input")
	}
	if _, err := budget.ReserveProgramState(context.Background(), 17<<20); err == nil {
		t.Fatal("accepted excessive state")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := budget.ReserveProgramState(ctx, 1); err == nil {
		t.Fatal("ignored cancellation")
	}
}

func TestActivityStructureKeepsStandaloneFailureAndBackgroundLaunch(t *testing.T) {
	process := activityFixture("process", "root", "command", "external", 0)
	helper := activityFixture("helper", "root", "sort", "sort", 10)
	job := activityFixture("job", "root", "program &", "", 20)
	job.receipt.JobHandle = "job:one"
	groups := activityGroups(process, helper, job)
	groups[1].counts.failed = 1
	values := composeActivityGroups(groups, true)
	if len(values) != 3 || values[1].FailedCount != 1 || values[2].JobHandle != "job:one" || values[2].Outcome != "Started" || values[2].Phase != "execute" {
		t.Fatalf("lost host outcome: %+v", values)
	}
}

func TestSequentialSamePhaseOperationsFoldIntoOneRow(t *testing.T) {
	find := activityFixture("find", "root", "find", "find", 0)
	grep := activityFixture("grep", "root", "grep", "grep", 10)
	grep.family = "search"
	grep.arguments["path_targets"] = map[string]any{"read": []any{"a.go"}}
	sed := activityFixture("sed", "root", "sed", "sed", 20)
	sed.family = "sed"
	sed.arguments["path_targets"] = map[string]any{"read": []any{"b.go"}}
	live := composeActivityGroups(activityGroups(find, grep), false)
	settled := composeActivityGroups(activityGroups(find, grep, sed), true)
	if len(live) != 1 || len(settled) != 1 || live[0].ID != settled[0].ID || settled[0].ID != "find" {
		t.Fatalf("phase folding moved the row: live=%+v settled=%+v", live, settled)
	}
	if settled[0].Group != "Exploring project files" || settled[0].Phase != "inspect" || len(settled[0].Steps) != 3 || len(settled[0].ExecutionIDs) != 3 {
		t.Fatalf("folded membership: %+v", settled[0])
	}
}

func TestPhaseTransitionsKeepSeparateRows(t *testing.T) {
	read := activityFixture("sed", "root", "sed", "sed", 0)
	read.family = "sed"
	read.arguments["path_targets"] = map[string]any{"read": []any{"a.go"}}
	write := activityFixture("write", "root", "write", "write", 10)
	write.receipt.EffectClasses = []string{"mutation"}
	build := activityFixture("cargo", "root", "command", "external", 20)
	build.arguments["command"] = "cargo build"
	commit := activityFixture("commit", "root", "git_commit", "git commit", 30)
	values := composeActivityGroups(activityGroups(read, write, build, commit), true)
	if len(values) != 4 {
		t.Fatalf("phase transitions collapsed: %+v", values)
	}
	for i, phase := range []string{"inspect", "mutate", "execute", "mutate"} {
		if values[i].Phase != phase {
			t.Fatalf("phase %d = %q, want %q", i, values[i].Phase, phase)
		}
	}
}

func TestExternalProcessesAndLaunchesStaySeparateRows(t *testing.T) {
	build := activityFixture("build", "root", "command", "external", 0)
	build.arguments["command"] = "cargo build"
	test := activityFixture("test", "root", "command", "external", 10)
	test.arguments["command"] = "cargo test"
	first := activityFixture("job1", "root", "program &", "", 20)
	first.receipt.JobHandle = "job:one"
	second := activityFixture("job2", "root", "program &", "", 30)
	second.receipt.JobHandle = "job:two"
	values := composeActivityGroups(activityGroups(build, test, first, second), true)
	if len(values) != 4 || values[0].Subject != "cargo build" || values[1].Subject != "cargo test" || values[2].JobHandle != "job:one" || values[3].JobHandle != "job:two" || values[2].Steps[0].Command != "Start background job" {
		t.Fatalf("external work folded: %+v", values)
	}
}

func TestActivityStepsCarryActionLabels(t *testing.T) {
	read := activityFixture("sed", "root", "sed", "sed", 0)
	read.family = "sed"
	read.arguments["path_targets"] = map[string]any{"read": []any{"b.go"}}
	write := activityFixture("write", "root", "write", "write", 10)
	write.receipt.EffectClasses = []string{"mutation"}
	values := composeActivityGroups(activityGroups(read, write), true)
	if values[0].Steps[0].Command != "Read source · b.go" || values[1].Steps[0].Command != "Change files · write" {
		t.Fatalf("step labels: %+v %+v", values[0].Steps[0], values[1].Steps[0])
	}
}
