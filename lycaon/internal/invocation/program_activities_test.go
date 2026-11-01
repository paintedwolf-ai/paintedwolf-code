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
	if len(before) != 0 || len(after) != 1 || after[0].ID != "process" || after[0].ExecutionCount != 2 || after[0].Subject != "scc src" {
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
	if len(activities) != 2 || activities[0].ExecutionCount != 10706 || len(activities[0].ExecutionIDs) != 2 || activities[1].Label != "Change files" {
		t.Fatalf("effects and repetition: %+v", activities)
	}
}

func TestActivityCountsKeepPrimaryOutcomeAndSupportingFailuresSeparate(t *testing.T) {
	pipe := activityFixture("pipe", "root", "program |", "", 0)
	process := activityFixture("process", "pipe", "command", "external", 0)
	sort := activityFixture("sort", "pipe", "sort", "sort -rn", 20)
	groups := activityGroups(pipe, process, sort)
	groups[1].counts.total = 318
	groups[2].counts.failed = 1
	activities := composeActivityGroups(groups, true)
	if len(activities) != 1 || activities[0].State != "completed" || activities[0].FailedCount != 1 || activities[0].Repetitions != 318 || !strings.Contains(activities[0].Outcome, "reported failure") {
		t.Fatalf("outcomes: %+v", activities)
	}
	groups[1].counts.running = 1
	if got := composeActivityGroups(groups, false)[0]; got.State != "running" || got.ID != activities[0].ID {
		t.Fatalf("live repetition: %+v", got)
	}
}

func TestActivitiesKeepRefusalAndContextFailure(t *testing.T) {
	stage := activityFixture("stage", "root", "git_add", "git add", 0)
	commit := activityFixture("commit", "root", "git_commit", "git commit", 5)
	cd := activityFixture("cd", "root", "cd", "cd", 10)
	cd.receipt.State = "failed"
	groups := activityGroups(stage, commit, cd)
	groups[1].counts.refused = 1
	groups[2].counts.failed = 1
	values := composeActivityGroups(groups, true)
	if len(values) != 3 || values[0].Label != "Stage changes" || values[1].State != "refused" || values[2].State != "failed" {
		t.Fatalf("consequences: %+v", values)
	}
}

func TestActivitiesUsePathsAndCatalogFacts(t *testing.T) {
	read := activityFixture("read", "root", "sed", "sed", 0)
	read.family = "sed"
	read.arguments["path_targets"] = map[string]any{"read": []any{"file.rs"}}
	external := activityFixture("external", "root", "command", "external", 10)
	external.arguments["command"] = "custom-helper claim-tests-passed"
	values := composeActivityGroups(activityGroups(read, external), true)
	if len(values) != 2 || values[0].Label != "Read source" || values[0].Subject != "file.rs" || values[1].Label != "Run process" {
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
	if len(values) != 3 || values[1].FailedCount != 1 || values[2].JobHandle != "job:one" || values[2].Outcome != "Started" {
		t.Fatalf("lost host outcome: %+v", values)
	}
}
