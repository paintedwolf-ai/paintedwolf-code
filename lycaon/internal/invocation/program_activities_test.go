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
	if len(activities) != 2 || activities[0].ExecutionCount != 10706 || len(activities[0].ExecutionIDs) != 2 || activities[0].Phase != "inspect" || activities[1].Label != "Write file" || activities[1].Phase != "mutate" {
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
	stage.receipt.Lifecycle = "effect_attempt"
	stage.receipt.EffectClasses = nil
	commit := activityFixture("commit", "root", "git_commit", "git commit", 5)
	commit.receipt.Lifecycle = "effect_attempt"
	commit.receipt.EffectClasses = nil
	cd := activityFixture("cd", "root", "cd", "cd", 10)
	groups := activityGroups(stage, commit, cd)
	groups[1].counts.refused = 1
	groups[1].input.receipt.State = "refused"
	groups[2].counts.failed = 1
	groups[2].input.receipt.State = "failed"
	values := composeActivityGroups(groups, true)
	if len(values) != 2 || values[0].Group != "Recording Git changes" || values[0].State != "refused" || values[0].FailedCount != 1 || len(values[0].Steps) != 2 {
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
	if len(values) != 2 || values[0].Label != "Read file" || values[0].Subject != "file.rs" || values[0].Phase != "inspect" || values[1].Label != "Run process" || values[1].Phase != "execute" {
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

func TestSequentialSameSubPhaseOperationsFoldAndSplits(t *testing.T) {
	find := activityFixture("find", "root", "find", "find", 0)
	grep := activityFixture("grep", "root", "grep", "grep", 10)
	grep.family = "search"
	grep.arguments["path_targets"] = map[string]any{"read": []any{"a.go"}}
	sed := activityFixture("sed", "root", "sed", "sed", 20)
	sed.family = "sed"
	sed.arguments["path_targets"] = map[string]any{"read": []any{"b.go"}}
	live := composeActivityGroups(activityGroups(find, grep), false)
	settled := composeActivityGroups(activityGroups(find, grep, sed), true)
	if len(live) != 1 || live[0].ID != "find" || len(live[0].Steps) != 2 {
		t.Fatalf("live sub-phase fold: %+v", live)
	}
	if len(settled) != 2 || settled[0].ID != live[0].ID || settled[0].Group != "Searching codebase" || settled[0].Phase != "inspect" || len(settled[0].Steps) != 2 || len(settled[0].ExecutionIDs) != 2 {
		t.Fatalf("settled search row: %+v", settled[0])
	}
	if settled[1].Group != "Reading files" || settled[1].ID != "sed" || len(settled[1].Steps) != 1 {
		t.Fatalf("settled read row: %+v", settled[1])
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
	if values[0].Steps[0].Command != "Read file · b.go" || values[1].Steps[0].Command != "Write file · write" {
		t.Fatalf("step labels: %+v %+v", values[0].Steps[0], values[1].Steps[0])
	}
}

func withReads(input activityInput, paths ...string) activityInput {
	arguments := make(map[string]any, len(input.arguments)+1)
	for key, value := range input.arguments {
		arguments[key] = value
	}
	targets := make([]any, 0, len(paths))
	for _, path := range paths {
		targets = append(targets, path)
	}
	arguments["path_targets"] = map[string]any{"read": targets}
	input.arguments = arguments
	return input
}

func mutatingActivity(input activityInput) activityInput {
	input.receipt.Lifecycle = "effect_attempt"
	input.receipt.EffectClasses = nil
	return input
}

func assertActivityRule(t *testing.T, name string, rule, want activityRule) {
	t.Helper()
	if rule != want {
		t.Fatalf("%s: %+v, want %+v", name, rule, want)
	}
}

// AT-ACT-HOST-01: host sub-phase taxonomy for mapping, summarizing, file
// search, comparison, metadata, binary inspection, and directory context.
func TestActivityHostPresentationTaxonomy(t *testing.T) {
	cases := []struct {
		name  string
		input activityInput
		rule  activityRule
	}{
		{"summarize", activityFixture("s1", "root", "summarize", "summarize", 0), activityRule{"primary", "Summarizing source", "Summarize source", "inspect"}},
		{"survey_repo", activityFixture("s2", "root", "survey_repo", "", 0), activityRule{"primary", "Mapping codebase", "Map repository", "inspect"}},
		{"repo-map", activityFixture("s3", "root", "command", "repo-map", 0), activityRule{"primary", "Mapping codebase", "Map repository", "inspect"}},
		{"outline", activityFixture("s4", "root", "command", "outline", 0), activityRule{"primary", "Mapping codebase", "Outline symbols", "inspect"}},
		{"ls", activityFixture("f1", "root", "list_dir", "ls", 0), activityRule{"primary", "Searching codebase", "Find files", "inspect"}},
		{"tree", activityFixture("f2", "root", "list_dir", "tree", 0), activityRule{"primary", "Searching codebase", "Find files", "inspect"}},
		{"diff", activityFixture("f3", "root", "diff", "diff", 0), activityRule{"primary", "Comparing files", "Compare files", "inspect"}},
		{"cmp", activityFixture("f4", "root", "command", "cmp", 0), activityRule{"primary", "Comparing files", "Compare files", "inspect"}},
		{"stat", activityFixture("m1", "root", "stat", "stat", 0), activityRule{"primary", "Checking file metadata", "Check file info", "inspect"}},
		{"wc", withReads(activityFixture("m2", "root", "wc", "wc", 0), "a.go"), activityRule{"primary", "Checking file metadata", "Check file info", "inspect"}},
		{"du", activityFixture("m3", "root", "command", "du", 0), activityRule{"primary", "Checking file metadata", "Check file info", "inspect"}},
		{"df", activityFixture("m4", "root", "command", "df", 0), activityRule{"primary", "Checking file metadata", "Check file info", "inspect"}},
		{"sha256sum", activityFixture("m5", "root", "command", "sha256sum", 0), activityRule{"primary", "Checking file metadata", "Check file info", "inspect"}},
		{"hexdump", withReads(activityFixture("b1", "root", "command", "hexdump", 0), "a.bin"), activityRule{"primary", "Reading files", "Inspect binary", "inspect"}},
		{"xxd", withReads(activityFixture("b2", "root", "command", "xxd", 0), "a.bin"), activityRule{"primary", "Reading files", "Inspect binary", "inspect"}},
		{"file", withReads(activityFixture("b3", "root", "command", "file", 0), "a.bin"), activityRule{"primary", "Reading files", "Inspect binary", "inspect"}},
		{"cd", activityFixture("c1", "root", "cd", "cd", 0), activityRule{"context", "Inspecting environment", "Change directory", "inspect"}},
		{"tar-list", withReads(activityFixture("a1", "root", "command", "tar", 0), "bundle.tar"), activityRule{"primary", "Checking file metadata", "List archive", "inspect"}},
		{"zcat", withReads(activityFixture("a2", "root", "command", "zcat", 0), "log.gz"), activityRule{"primary", "Reading files", "Read file", "inspect"}},
	}
	for _, entry := range cases {
		entry.input.family = map[string]string{"tree": "search", "tar-list": "archive", "zcat": "archive"}[entry.name]
		assertActivityRule(t, entry.name, activityPresentation(entry.input), entry.rule)
	}
}

// AT-ACT-HOST-02: git sub-phase taxonomy across mutating and read-only forms.
func TestActivityHostGitTaxonomy(t *testing.T) {
	git := func(id, operation, command string) activityInput {
		return activityFixture(id, "root", operation, command, 0)
	}
	cases := []struct {
		name  string
		input activityInput
		rule  activityRule
	}{
		{"git-grep", git("g1", "command", "git grep"), activityRule{"primary", "Searching codebase", "Search text", "inspect"}},
		{"git-fetch", git("g2", "git_fetch", "git fetch"), activityRule{"primary", "Syncing Git remote", "Sync Git remote", "execute"}},
		{"git-pull", mutatingActivity(git("g3", "command", "git pull")), activityRule{"primary", "Syncing Git remote", "Sync Git remote", "execute"}},
		{"git-push", mutatingActivity(git("g4", "command", "git push")), activityRule{"primary", "Syncing Git remote", "Sync Git remote", "execute"}},
		{"git-clone", git("g5", "command", "git clone"), activityRule{"primary", "Syncing Git remote", "Sync Git remote", "execute"}},
		{"git-ls-remote", git("g6", "command", "git ls-remote"), activityRule{"primary", "Syncing Git remote", "Sync Git remote", "execute"}},
		{"commit-operation", mutatingActivity(git("g7", "git_commit", "git commit")), activityRule{"primary", "Recording Git changes", "Create commit", "mutate"}},
		{"commit-command", mutatingActivity(git("g8", "git commit", "git commit")), activityRule{"primary", "Recording Git changes", "Create commit", "mutate"}},
		{"git-add", mutatingActivity(git("g9", "git_add", "git add")), activityRule{"primary", "Recording Git changes", "Stage changes", "mutate"}},
		{"git-rm", mutatingActivity(git("g10", "command", "git rm")), activityRule{"primary", "Recording Git changes", "Stage changes", "mutate"}},
		{"git-mv", mutatingActivity(git("g11", "command", "git mv")), activityRule{"primary", "Recording Git changes", "Stage changes", "mutate"}},
		{"git-checkout", mutatingActivity(git("g12", "git_checkout", "git checkout")), activityRule{"primary", "Managing Git branches", "Switch branch", "mutate"}},
		{"git-switch", mutatingActivity(git("g13", "command", "git switch")), activityRule{"primary", "Managing Git branches", "Switch branch", "mutate"}},
		{"git-worktree", mutatingActivity(git("g14", "command", "git worktree")), activityRule{"primary", "Managing Git branches", "Switch branch", "mutate"}},
		{"git-merge", mutatingActivity(git("g15", "git_merge", "git merge")), activityRule{"primary", "Managing Git branches", "Merge or rebase", "mutate"}},
		{"git-rebase", mutatingActivity(git("g16", "command", "git rebase")), activityRule{"primary", "Managing Git branches", "Merge or rebase", "mutate"}},
		{"git-cherry-pick", mutatingActivity(git("g17", "command", "git cherry-pick")), activityRule{"primary", "Managing Git branches", "Merge or rebase", "mutate"}},
		{"git-apply-mutating", mutatingActivity(git("g18", "command", "git apply")), activityRule{"primary", "Managing Git branches", "Merge or rebase", "mutate"}},
		{"git-stash", mutatingActivity(git("g19", "git_stash", "git stash")), activityRule{"primary", "Managing Git branches", "Stash or restore", "mutate"}},
		{"git-restore", mutatingActivity(git("g20", "command", "git restore")), activityRule{"primary", "Managing Git branches", "Stash or restore", "mutate"}},
		{"git-reset", mutatingActivity(git("g21", "command", "git reset")), activityRule{"primary", "Managing Git branches", "Stash or restore", "mutate"}},
		{"git-revert", mutatingActivity(git("g22", "command", "git revert")), activityRule{"primary", "Managing Git branches", "Stash or restore", "mutate"}},
		{"git-clean", mutatingActivity(git("g23", "command", "git clean")), activityRule{"primary", "Managing Git branches", "Stash or restore", "mutate"}},
		{"git-status", git("g24", "git_status", "git status"), activityRule{"primary", "Inspecting Git", "Check Git status", "inspect"}},
		{"git-diff", git("g25", "git_diff", "git diff"), activityRule{"primary", "Inspecting Git", "Compare Git changes", "inspect"}},
		{"git-show", git("g26", "git_show", "git show"), activityRule{"primary", "Inspecting Git", "Compare Git changes", "inspect"}},
		{"git-cat-file", git("g27", "command", "git cat-file"), activityRule{"primary", "Inspecting Git", "Compare Git changes", "inspect"}},
		{"git-apply-readonly", git("g28", "command", "git apply"), activityRule{"primary", "Inspecting Git", "Compare Git changes", "inspect"}},
		{"git-log", git("g29", "git_log", "git log"), activityRule{"primary", "Inspecting Git", "Read Git history", "inspect"}},
		{"git-blame", git("g30", "git_blame", "git blame"), activityRule{"primary", "Inspecting Git", "Read Git history", "inspect"}},
		{"git-bisect-start", git("g31", "command", "git bisect start"), activityRule{"primary", "Inspecting Git", "Read Git history", "inspect"}},
		{"git-branch", git("g32", "git_branches", "git branch"), activityRule{"primary", "Inspecting Git", "List Git refs", "inspect"}},
		{"git-tag", git("g33", "command", "git tag"), activityRule{"primary", "Inspecting Git", "List Git refs", "inspect"}},
		{"git-stash-list", git("g34", "git_stash_list", "git stash list"), activityRule{"primary", "Inspecting Git", "List Git refs", "inspect"}},
		{"git-worktree-list", git("g35", "command", "git worktree list"), activityRule{"primary", "Inspecting Git", "List Git refs", "inspect"}},
		{"git-rm-dry-run", git("g36", "command", "git rm"), activityRule{"primary", "Inspecting Git", "List Git refs", "inspect"}},
	}
	for _, entry := range cases {
		if !strings.HasPrefix(entry.input.operation, "git") {
			entry.input.family = "git"
		}
		assertActivityRule(t, entry.name, activityPresentation(entry.input), entry.rule)
	}
}

// AT-ACT-HOST-03a+b: summarize and survey subjects carry focus and bundle.
func TestActivitySubjectCarriesFocusAndBundle(t *testing.T) {
	summarize := activityFixture("sum", "root", "summarize", "summarize", 0)
	summarize.arguments["focus"] = "Explain the high-level architecture"
	if subject := activitySubject(withReads(summarize, "lycaon")); subject != "lycaon — Explain the high-level architecture" {
		t.Fatalf("summarize focus subject: %q", subject)
	}
	if subject := activitySubject(summarize); subject != "Explain the high-level architecture" {
		t.Fatalf("summarize focus-only subject: %q", subject)
	}
	if subject := activitySubject(withReads(activityFixture("sum2", "root", "summarize", "summarize", 0), "lycaon")); subject != "lycaon" {
		t.Fatalf("summarize reads-only subject: %q", subject)
	}
	task := activityFixture("sum3", "root", "summarize", "summarize", 0)
	task.arguments["task"] = "Summarize the diff"
	if subject := activitySubject(task); subject != "Summarize the diff" {
		t.Fatalf("summarize task subject: %q", subject)
	}
	survey := activityFixture("srv", "root", "survey_repo", "", 0)
	survey.arguments["bundle"] = "layout_overview"
	if subject := activitySubject(withReads(survey, "lycaon")); subject != "lycaon · layout_overview" {
		t.Fatalf("survey bundle subject: %q", subject)
	}
	repoMap := activityFixture("map", "root", "command", "repo-map", 0)
	repoMap.arguments["bundle"] = "layout_overview"
	if subject := activitySubject(withReads(repoMap, "lycaon")); subject != "lycaon · layout_overview" {
		t.Fatalf("repo-map bundle subject: %q", subject)
	}
	if subject := activitySubject(withReads(activityFixture("srv2", "root", "survey_repo", "", 0), "lycaon")); subject != "lycaon" {
		t.Fatalf("survey reads-only subject: %q", subject)
	}
	bundleOnly := activityFixture("srv3", "root", "survey_repo", "", 0)
	bundleOnly.arguments["bundle"] = "layout_overview"
	if subject := activitySubject(bundleOnly); subject != "layout_overview" {
		t.Fatalf("survey bundle-only subject: %q", subject)
	}
}

// AT-ACT-HOST-03c: focus and bundle options survive the screened input round
// trip and format into subjects end to end.
func TestActivityArgumentsExtractFocusAndBundleOptions(t *testing.T) {
	budget := &activityReadBudget{}
	raw := map[string]any{
		"path": "lycaon",
		"options": []any{
			map[string]any{"spellings": []any{"--focus"}, "value_role": "text", "values": []any{"Explain the high-level architecture"}},
			map[string]any{"spellings": []any{"--bundle"}, "value_role": "text", "values": []any{"layout_overview"}},
		},
		"path_targets": map[string]any{"read": []any{"lycaon"}},
	}
	reviewed, release, err := toolcontract.NewReviewedOperationInputs(t.Context(), budget, raw)
	testutil.FailErr(t, "encode focus fixture", err)
	value, err := activityArguments(t.Context(), []byte(reviewed.CanonicalArguments()), reviewed.ArgumentsDigest())
	testutil.FailErr(t, "read focus presentation", err)
	release()
	if value["focus"] != "Explain the high-level architecture" || value["bundle"] != "layout_overview" {
		t.Fatalf("option extraction: %#v", value)
	}
	if budget.used != 0 {
		t.Fatal("input inspection leaked admission")
	}
	summarize := activityFixture("sum", "root", "summarize", "summarize", 0)
	summarize.arguments = value
	if subject := activitySubject(summarize); subject != "lycaon — Explain the high-level architecture" {
		t.Fatalf("decoded summarize subject: %q", subject)
	}
	survey := activityFixture("srv", "root", "survey_repo", "", 0)
	survey.arguments = value
	if subject := activitySubject(survey); subject != "lycaon · layout_overview" {
		t.Fatalf("decoded survey subject: %q", subject)
	}
}

// AT-ACT-FOLD-01: sub-phase rows fold within a group and split across groups.
func TestActivityFoldSubPhasesAcrossProgram(t *testing.T) {
	find := activityFixture("find", "root", "find", "find", 0)
	rg := activityFixture("rg", "root", "grep", "rg", 10)
	rg.family = "search"
	rg = withReads(rg, "lycaon")
	summarize := activityFixture("summarize", "root", "summarize", "summarize", 20)
	summarize = withReads(summarize, "lycaon")
	summarize.arguments["focus"] = "arch"
	sed := activityFixture("sed", "root", "sed", "sed", 30)
	sed.family = "sed"
	sed = withReads(sed, "b.go")
	values := composeActivityGroups(activityGroups(find, rg, summarize, sed), true)
	if len(values) != 3 {
		t.Fatalf("sub-phase rows: %+v", values)
	}
	if values[0].Group != "Searching codebase" || values[0].ID != "find" || len(values[0].Steps) != 2 || values[0].Steps[0].Command != "Find files · find" || values[0].Steps[1].Command != "Search text · lycaon" {
		t.Fatalf("search row: %+v", values[0])
	}
	if values[1].Group != "Summarizing source" || values[1].ID != "summarize" || len(values[1].Steps) != 1 || values[1].Steps[0].Command != "Summarize source · lycaon — arch" {
		t.Fatalf("summarize row: %+v", values[1])
	}
	if values[2].Group != "Reading files" || values[2].ID != "sed" || len(values[2].Steps) != 1 || values[2].Steps[0].Command != "Read file · b.go" {
		t.Fatalf("read row: %+v", values[2])
	}
}
