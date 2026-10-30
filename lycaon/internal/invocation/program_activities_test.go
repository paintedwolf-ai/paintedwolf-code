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
	return activityInput{operation: operation, pipeline: parent != "root", arguments: map[string]any{}, receipt: api.CommandExecutionReceipt{ID: id, ParentExecutionID: parent, NativeCommand: command, Command: command, State: "completed", StatusCode: &zero, Lifecycle: "read_only"}, span: api.CommandActivitySpan{SourceRef: "source", StartByte: start, EndByte: start + 3}}
}

func TestActivitiesSplitExternalProcessesAndAttachOutputSteps(t *testing.T) {
	build := activityFixture("build", "pipe-build", "command", "external", 0)
	build.arguments["command"] = "cargo build --release"
	build.receipt.EffectClasses = []string{"external_exec"}
	tail := activityFixture("tail", "pipe-build", "read", "tail", 20)
	test := activityFixture("test", "pipe-test", "command", "external", 30)
	test.arguments["command"] = "cargo test"
	grep := activityFixture("grep", "pipe-test", "grep", "grep", 40)
	head := activityFixture("head", "pipe-test", "read", "head", 50)
	activities := composeActivities([]activityInput{build, tail, test, grep, head})
	if len(activities) != 2 || activities[0].Subject != "cargo build --release" || activities[1].Subject != "cargo test" || len(activities[1].ExecutionIDs) != 3 {
		t.Fatalf("activities = %+v", activities)
	}
	if activities[1].Label != "Run process" || activities[1].Outcome != "Exited 0" {
		t.Fatalf("invented semantic outcome: %+v", activities[1])
	}
}

func TestActivitiesKeepMutationRefusalAndContextFailure(t *testing.T) {
	stage := activityFixture("stage", "root", "git_add", "git add", 0)
	stage.receipt.EffectClasses = []string{"mutation"}
	commit := activityFixture("commit", "root", "git_commit", "git commit", 5)
	commit.receipt.State = "refused"
	cd := activityFixture("cd", "root", "cd", "cd", 10)
	cd.receipt.State = "failed"
	values := composeActivities([]activityInput{stage, commit, cd})
	if len(values) != 3 || values[0].Label != "Stage changes" || values[1].Outcome != "Refused" || values[2].State != "failed" {
		t.Fatalf("consequences disappeared: %+v", values)
	}
}

func TestActivitiesUsePathsAndCatalogFamilyWithoutReadingOutput(t *testing.T) {
	read := activityFixture("read", "pipe", "sed", "sed", 0)
	read.family = "sed"
	read.arguments["path_targets"] = map[string]any{"read": []any{"file.rs"}}
	filter := activityFixture("filter", "pipe", "read", "head", 5)
	external := activityFixture("external", "root", "command", "external", 10)
	external.arguments["command"] = "custom-helper claim-tests-passed"
	values := composeActivities([]activityInput{read, filter, external})
	if len(values) != 2 || values[0].Label != "Read source" || values[0].Subject != "file.rs" || values[1].Label != "Run process" {
		t.Fatalf("classification = %+v", values)
	}
}

func TestActivitiesRetainControlOnlyAndUnknownOperations(t *testing.T) {
	control := activityFixture("cd", "root", "cd", "cd", 0)
	values := composeActivities([]activityInput{control})
	if len(values) != 1 {
		t.Fatal("control-only execution disappeared")
	}
	unknown := activityFixture("unknown", "root", "extension", "extension", 0)
	unknown.receipt.Lifecycle = "effect_attempt"
	if got := composeActivities([]activityInput{unknown})[0]; got.Label != "Run command" {
		t.Fatalf("unknown operation = %+v", got)
	}
	for _, state := range []string{"prepared", "waiting", "refused", "failed", "interrupted", "unknown"} {
		unknown.receipt.State = state
		_, outcome := activityOutcome(unknown.receipt)
		if outcome == "" {
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
		t.Fatalf("bounded projection = %#v", value)
	}
	if budget.used != 0 {
		t.Fatal("input inspection leaked admission")
	}
	if _, err := activityArguments(t.Context(), []byte("[]"), "wrong"); err == nil {
		t.Fatal("accepted malformed retained input")
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
