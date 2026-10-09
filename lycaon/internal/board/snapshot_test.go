package board

import (
	"context"
	"errors"
	repotest "github.com/lycaon/lycaon/internal/testsetup/repoinfo"
	"path/filepath"
	"testing"
	"time"

	"github.com/lycaon/lycaon/config"
	"github.com/lycaon/lycaon/config/configtest"
	"github.com/lycaon/lycaon/internal/delegation"
	"github.com/lycaon/lycaon/internal/orchestration"
	"github.com/lycaon/lycaon/internal/project"
	"github.com/lycaon/lycaon/internal/repoinfo"
	"github.com/lycaon/lycaon/internal/scan"
	"github.com/lycaon/lycaon/internal/settings"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/worker"
	"github.com/lycaon/lycaon/pkg/api"
)

type progressiveRepoProvider struct {
	brief *repoinfo.Brief
}

func (p progressiveRepoProvider) Brief(context.Context, string) (*repoinfo.Brief, error) {
	return p.brief, nil
}

func (progressiveRepoProvider) KnownEmpty(context.Context, string) (bool, error) { return false, nil }
func (progressiveRepoProvider) Warm(string)                                      {}
func (progressiveRepoProvider) Changed(context.Context, string)                  {}
func (progressiveRepoProvider) SetOnSettled(func(string))                        {}
func (progressiveRepoProvider) Close() error                                     { return nil }

func TestSnapshotBuilderWithholdsProgressiveRepoBrief(t *testing.T) {
	b := &SnapshotBuilder{Repo: progressiveRepoProvider{brief: &repoinfo.Brief{
		Languages:   []string{"Go"},
		FileCount:   1234,
		GeneratedAt: time.Now().UTC(),
	}}}
	snap, err := b.Build(t.Context(), testdbseed.DefaultProjectID, t.TempDir(), "sess-1", api.BoardDetailLevelCompact, nil)
	testutil.FailErr(t, "build", err)
	if snap.Repo.FileCount != 0 || len(snap.Repo.Languages) != 0 || !snap.Repo.GeneratedAt.IsZero() {
		t.Fatalf("board exposed progressive repo estimate: %+v", snap.Repo)
	}
}

func TestSnapshotBuilderBuild_Empty(t *testing.T) {
	b := &SnapshotBuilder{
		Delegations: delegation.NewMemoryStore(),
		Workers:     worker.NewInMemoryQueue(10),
		Repo:        repotest.NewProvider(t),
	}
	ctx := context.Background()
	dir := t.TempDir()

	snap, err := b.Build(ctx, testdbseed.DefaultProjectID, dir, "sess-1", api.BoardDetailLevelCompact, nil)
	testutil.FailErr(t, "b.Build failed", err)
	if snap.Repo.FileCount != 0 {
		t.Fatalf("Repo.FileCount = %d, want 0 for empty dir", snap.Repo.FileCount)
	}
	if len(snap.Repo.Languages) != 0 {
		t.Fatalf("Repo.Languages = %v, want empty", snap.Repo.Languages)
	}
	if snap.DetailLevel != api.BoardDetailLevelCompact {
		t.Fatalf("DetailLevel = %q", snap.DetailLevel)
	}
	if snap.Delegation == nil || snap.Workers == nil {
		t.Fatal("Delegation or Workers slice is nil")
	}
	delegations, _ := (*snap.Delegation)["delegations"].([]api.Delegation)
	tasks, _ := (*snap.Workers)["tasks"].([]api.WorkerTask)
	if len(delegations) != 0 || len(tasks) != 0 {
		t.Fatalf("delegations = %d tasks = %d, want 0 each", len(delegations), len(tasks))
	}
}

func TestSnapshotBuilderBuild_NilStores(t *testing.T) {
	b := &SnapshotBuilder{Repo: repotest.NewProvider(t)}
	ctx := context.Background()

	snap, err := b.Build(ctx, testdbseed.DefaultProjectID, t.TempDir(), "sess-1", api.BoardDetailLevelCompact, nil)
	testutil.FailErr(t, "b.Build failed", err)
	if snap.Repo.FileCount != 0 {
		t.Fatalf("Repo.FileCount = %d, want 0", snap.Repo.FileCount)
	}
	delegations, _ := (*snap.Delegation)["delegations"].([]api.Delegation)
	tasks, _ := (*snap.Workers)["tasks"].([]api.WorkerTask)
	if len(delegations) != 0 || len(tasks) != 0 {
		t.Fatalf("delegations = %d tasks = %d, want 0 each", len(delegations), len(tasks))
	}
}

func TestSnapshotBuilderBuild_DefaultDetailLevel(t *testing.T) {
	b := &SnapshotBuilder{Repo: repotest.NewProvider(t)}
	ctx := context.Background()

	snap, err := b.Build(ctx, testdbseed.DefaultProjectID, t.TempDir(), "sess-1", "", nil)
	testutil.FailErr(t, "b.Build failed", err)
	if snap.DetailLevel != api.BoardDetailLevelCompact {
		t.Fatalf("DetailLevel = %q, want compact", snap.DetailLevel)
	}
}

func TestSnapshotBuilderBuild_IncludesDelegationsAndWorkers(t *testing.T) {
	delegationStore := delegation.NewMemoryStore()
	queue := worker.NewInMemoryQueue(10)
	b := &SnapshotBuilder{Delegations: delegationStore, Workers: queue, Repo: repotest.NewProvider(t)}
	ctx := context.Background()
	dir := t.TempDir()
	otherDir := t.TempDir()

	if _, err := delegationStore.Create(ctx, api.Delegation{ProjectID: testdbseed.DefaultProjectID, WorkspacePath: dir, Task: "ship it"}, "sess-1", []api.Leg{{}}); err != nil {
		testutil.FailErr(t, "delegationStore.Create failed", err)
	}
	if _, err := delegationStore.Create(ctx, api.Delegation{ProjectID: testdbseed.DefaultProjectID, WorkspacePath: otherDir, Task: "other"}, "sess-2", []api.Leg{{}}); err != nil {
		testutil.FailErr(t, "delegationStore.Create failed", err)
	}
	if _, err := queue.EnqueueWithProjectID(ctx, testdbseed.DefaultProjectID, api.WorkerTask{
		ParentSessionID: "sess-1",
		AgentType:       orchestration.ProfileImplementer,
		ProjectID:       testdbseed.DefaultProjectID,
		WorkspacePath:   dir,
		Prompt:          "do work",
		Brief:           "fixture",
		ExecutionTarget: api.ExecutionTargetLocal,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := queue.EnqueueWithProjectID(ctx, testdbseed.DefaultProjectID, api.WorkerTask{
		ParentSessionID: "sess-2",
		AgentType:       orchestration.ProfileImplementer,
		ProjectID:       testdbseed.DefaultProjectID,
		WorkspacePath:   otherDir,
		Prompt:          "other work",
		Brief:           "fixture",
		ExecutionTarget: api.ExecutionTargetLocal,
	}); err != nil {
		t.Fatal(err)
	}

	snap, err := b.Build(ctx, testdbseed.DefaultProjectID, dir, "sess-1", api.BoardDetailLevelFull, nil)
	testutil.FailErr(t, "b.Build failed", err)
	if snap.DetailLevel != api.BoardDetailLevelFull {
		t.Fatalf("DetailLevel = %q", snap.DetailLevel)
	}

	delegations, ok := (*snap.Delegation)["delegations"].([]api.Delegation)
	if !ok {
		t.Fatal("delegations has unexpected type")
	}
	if len(delegations) != 1 {
		t.Fatalf("delegations = %d, want 1", len(delegations))
	}
	if delegations[0].Task != "ship it" {
		t.Fatalf("delegation task = %q", delegations[0].Task)
	}

	tasks, ok := (*snap.Workers)["tasks"].([]api.WorkerTask)
	if !ok {
		t.Fatal("tasks has unexpected type")
	}
	if len(tasks) != 1 {
		t.Fatalf("tasks = %d, want 1", len(tasks))
	}
	if tasks[0].Prompt != "do work" {
		t.Fatalf("task prompt = %q", tasks[0].Prompt)
	}
}

func TestSnapshotBuilderBuild_WorkersScopedToSession(t *testing.T) {
	queue := worker.NewInMemoryQueue(10)
	b := &SnapshotBuilder{Workers: queue, Repo: repotest.NewProvider(t)}
	ctx := context.Background()
	dir := t.TempDir()

	if _, err := queue.EnqueueWithProjectID(ctx, testdbseed.DefaultProjectID, api.WorkerTask{
		ParentSessionID: "sess-a",
		AgentType:       orchestration.ProfileImplementer,
		ProjectID:       testdbseed.DefaultProjectID,
		WorkspacePath:   dir,
		Prompt:          "alpha",
		Brief:           "fixture",
		ExecutionTarget: api.ExecutionTargetLocal,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := queue.EnqueueWithProjectID(ctx, testdbseed.DefaultProjectID, api.WorkerTask{
		ParentSessionID: "sess-b",
		AgentType:       orchestration.ProfileImplementer,
		ProjectID:       testdbseed.DefaultProjectID,
		WorkspacePath:   dir,
		Prompt:          "beta",
		Brief:           "fixture",
		ExecutionTarget: api.ExecutionTargetLocal,
	}); err != nil {
		t.Fatal(err)
	}

	snapA, err := b.Build(ctx, testdbseed.DefaultProjectID, dir, "sess-a", api.BoardDetailLevelCompact, nil)
	testutil.FailErr(t, "b.Build sess-a failed", err)
	tasksA, _ := (*snapA.Workers)["tasks"].([]api.WorkerTask)
	if len(tasksA) != 1 || tasksA[0].Prompt != "alpha" {
		t.Fatalf("sess-a tasks = %#v, want single alpha worker", tasksA)
	}

	snapFresh, err := b.Build(ctx, testdbseed.DefaultProjectID, dir, "sess-new", api.BoardDetailLevelCompact, nil)
	testutil.FailErr(t, "b.Build sess-new failed", err)
	tasksFresh, _ := (*snapFresh.Workers)["tasks"].([]api.WorkerTask)
	if len(tasksFresh) != 0 {
		t.Fatalf("sess-new tasks = %d, want 0", len(tasksFresh))
	}
	if got := BuildBoardSummary(*snapFresh); got != "No workers." {
		t.Fatalf("summary = %q, want No workers.", got)
	}
}

func TestSnapshotBuilderBuild_DelegationsScopedToSession(t *testing.T) {
	delegationStore := delegation.NewMemoryStore()
	b := &SnapshotBuilder{Delegations: delegationStore, Repo: repotest.NewProvider(t)}
	ctx := context.Background()
	dir := t.TempDir()

	if _, err := delegationStore.Create(ctx, api.Delegation{ProjectID: testdbseed.DefaultProjectID, WorkspacePath: dir, Task: "alpha"}, "sess-a", []api.Leg{{}}); err != nil {
		testutil.FailErr(t, "delegationStore.Create alpha", err)
	}
	if _, err := delegationStore.Create(ctx, api.Delegation{ProjectID: testdbseed.DefaultProjectID, WorkspacePath: dir, Task: "beta"}, "sess-b", []api.Leg{{}}); err != nil {
		testutil.FailErr(t, "delegationStore.Create beta", err)
	}

	snapFresh, err := b.Build(ctx, testdbseed.DefaultProjectID, dir, "sess-new", api.BoardDetailLevelCompact, nil)
	testutil.FailErr(t, "b.Build sess-new failed", err)
	delegations, _ := (*snapFresh.Delegation)["delegations"].([]api.Delegation)
	if len(delegations) != 0 {
		t.Fatalf("sess-new delegations = %d, want 0", len(delegations))
	}

	snapA, err := b.Build(ctx, testdbseed.DefaultProjectID, dir, "sess-a", api.BoardDetailLevelCompact, nil)
	testutil.FailErr(t, "b.Build sess-a failed", err)
	delegationsA, _ := (*snapA.Delegation)["delegations"].([]api.Delegation)
	if len(delegationsA) != 1 || delegationsA[0].Task != "alpha" {
		t.Fatalf("sess-a delegations = %#v, want single alpha delegation", delegationsA)
	}
}

type stubWorkflowRunSource struct {
	active func(ctx context.Context, sessionID string) (*api.WorkflowRun, error)
}

func (s *stubWorkflowRunSource) ActiveBySession(ctx context.Context, sessionID string) (*api.WorkflowRun, error) {
	if s.active != nil {
		return s.active(ctx, sessionID)
	}
	return nil, nil
}

func (s *stubWorkflowRunSource) AttachRunUI(context.Context, *api.WorkflowRun) error {
	return nil
}

func TestSnapshotBuilderBuild_WorkflowRunScopedToSession(t *testing.T) {
	workflow := &stubWorkflowRunSource{
		active: func(_ context.Context, sessionID string) (*api.WorkflowRun, error) {
			if sessionID == "sess-a" {
				return &api.WorkflowRun{ID: "run-a", SessionID: "sess-a", Status: api.WorkflowRunStatusRunning}, nil
			}
			return nil, nil
		},
	}
	b := &SnapshotBuilder{Workflow: &WorkflowRunSource{Runs: workflow, Presentation: workflow}, Repo: repotest.NewProvider(t)}
	ctx := context.Background()
	dir := t.TempDir()

	snapA, err := b.Build(ctx, testdbseed.DefaultProjectID, dir, "sess-a", api.BoardDetailLevelCompact, nil)
	testutil.FailErr(t, "b.Build sess-a failed", err)
	if snapA.ActiveWorkflowRun == nil || snapA.ActiveWorkflowRun.ID != "run-a" {
		t.Fatalf("sess-a active run = %#v, want run-a", snapA.ActiveWorkflowRun)
	}

	snapFresh, err := b.Build(ctx, testdbseed.DefaultProjectID, dir, "sess-new", api.BoardDetailLevelCompact, nil)
	testutil.FailErr(t, "b.Build sess-new failed", err)
	if snapFresh.ActiveWorkflowRun != nil {
		t.Fatalf("sess-new active run = %#v, want nil", snapFresh.ActiveWorkflowRun)
	}
}

func TestSnapshotBuilderBuild_CanceledContext(t *testing.T) {
	b := &SnapshotBuilder{
		Delegations: delegation.NewMemoryStore(),
		Workers:     worker.NewInMemoryQueue(10),
		Repo:        repotest.NewProvider(t),
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := b.Build(ctx, testdbseed.DefaultProjectID, t.TempDir(), "sess-1", api.BoardDetailLevelCompact, nil)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
}

type stubScanSource struct {
	scans    []api.CodeScan
	previous []api.CodeScan
}

func (s *stubScanSource) LatestAssessmentForPaths(_ context.Context, _ []string) (scan.AssessmentView, error) {
	if len(s.scans) == 0 {
		return scan.AssessmentView{}, nil
	}
	members := append([]api.CodeScan(nil), s.scans...)
	assessmentID := members[0].AssessmentID
	if assessmentID == "" {
		assessmentID = members[0].ID
	}
	view := scan.AssessmentView{
		LatestAttemptID: assessmentID,
		LatestAttempt:   members,
		LatestSummary:   scan.BuildAssessmentSummary(assessmentID, members),
	}
	if members[0].Status == api.CodeScanStatusComplete {
		view.CurrentID = assessmentID
		view.Current = members
		view.CurrentSummary = view.LatestSummary
	}
	if len(s.previous) > 0 {
		view.Previous = append([]api.CodeScan(nil), s.previous...)
		view.PreviousID = view.Previous[0].AssessmentID
		if view.PreviousID == "" {
			view.PreviousID = view.Previous[0].ID
		}
	}
	return view, nil
}

func testSecurityScannersStore(t *testing.T, enabled bool) *settings.SecurityScannersStore {
	t.Helper()
	// Bundled defaults live in the binary; only the user overlay is a file on disk.
	configtest.Overlay(t, map[config.Rel]string{config.SecurityScanners: "enabled: true\n"})
	global := filepath.Join(t.TempDir(), "global.yaml")
	store, err := settings.NewSecurityScannersStoreAt(global)
	testutil.FailErr(t, "NewSecurityScannersStoreAt failed", err)
	if !enabled {
		disabled := false
		if err := store.PutGlobal(settings.SecurityScannersUserOverlay{Enabled: &disabled}); err != nil {
			testutil.FailErr(t, "PutGlobal failed", err)
		}
	}
	return store
}

func TestSnapshotBuilderLoadScansRespectsSecurityScannersEnabled(t *testing.T) {
	now := time.Date(2026, 6, 11, 12, 0, 0, 0, time.UTC)
	scan := api.CodeScan{
		ID:            "scan-clean",
		Status:        api.CodeScanStatusComplete,
		Categories:    []api.ScanCategory{api.ScanCategorySecurity},
		FindingsCount: 0,
		CreatedAt:     now,
		CompletedAt:   &now,
	}
	src := &stubScanSource{scans: []api.CodeScan{scan}}
	ctx := context.Background()
	dir := t.TempDir()

	enabledBuilder := &SnapshotBuilder{
		Repo:             repotest.NewProvider(t),
		Scans:            src,
		SecurityScanners: testSecurityScannersStore(t, true),
	}
	snap, err := enabledBuilder.Build(ctx, testdbseed.DefaultProjectID, dir, "sess-1", api.BoardDetailLevelCompact, nil)
	testutil.FailErr(t, "Build with scans enabled failed", err)
	if snap.Scans == nil || snap.Scans.CurrentAssessment == nil {
		t.Fatal("expected scans slice when security scans enabled")
	}
	if snap.Scans.CurrentAssessment.FindingsCount != 0 {
		t.Fatalf("FindingsCount = %d, want 0", snap.Scans.CurrentAssessment.FindingsCount)
	}

	disabledBuilder := &SnapshotBuilder{
		Repo:             repotest.NewProvider(t),
		Scans:            src,
		SecurityScanners: testSecurityScannersStore(t, false),
	}
	snapDisabled, err := disabledBuilder.Build(ctx, testdbseed.DefaultProjectID, dir, "sess-1", api.BoardDetailLevelCompact, nil)
	testutil.FailErr(t, "Build with scans disabled failed", err)
	if snapDisabled.Scans != nil {
		t.Fatal("expected nil scans slice when security scans disabled in Settings")
	}
}

type stubScanComparer struct {
	compare    *scan.Comparison
	compareErr error
}

func (s *stubScanComparer) BoardComparison(_ context.Context, _, _ api.CodeScan) (*scan.Comparison, error) {
	if s.compareErr != nil {
		return nil, s.compareErr
	}
	return s.compare, nil
}

func TestSnapshotBuilderLoadScansAttachesCompareSlice(t *testing.T) {
	now := time.Date(2026, 7, 13, 12, 0, 0, 0, time.UTC)
	latest := api.CodeScan{
		ID:            "scan-new",
		Status:        api.CodeScanStatusComplete,
		Categories:    []api.ScanCategory{api.ScanCategorySecurity},
		FindingsCount: 5,
		CreatedAt:     now,
		CompletedAt:   &now,
	}
	builder := &SnapshotBuilder{
		Repo: repotest.NewProvider(t),
		Scans: &stubScanSource{
			scans:    []api.CodeScan{latest},
			previous: []api.CodeScan{{ID: "scan-old"}},
		},
		ScanCompare: &stubScanComparer{
			compare: &scan.Comparison{
				NewCount:   2,
				NewByLevel: map[string]int{string(api.FindingLevelMedium): 2},
				NewFindings: []api.SecurityFinding{
					{RuleID: "r1", Locations: []api.SecurityFindingLocation{{URI: "a.go"}}},
				},
			},
		},
		SecurityScanners: testSecurityScannersStore(t, true),
	}
	snap, err := builder.Build(context.Background(), testdbseed.DefaultProjectID, t.TempDir(), "sess-1", api.BoardDetailLevelCompact, nil)
	testutil.FailErr(t, "Build", err)
	if snap.Scans == nil || snap.Scans.Compare == nil {
		t.Fatal("expected compare slice")
	}
	if snap.Scans.Compare.BaselineAssessmentID != "scan-old" || snap.Scans.Compare.NewCount != 2 {
		t.Fatalf("compare = %+v", snap.Scans.Compare)
	}
	if len(snap.Scans.Compare.TopNew) != 1 || snap.Scans.Compare.TopNew[0].File != "a.go" {
		t.Fatalf("top_new = %+v", snap.Scans.Compare.TopNew)
	}
}

func TestSnapshotBuilderLoadScansSkipsCompareWhenRunning(t *testing.T) {
	now := time.Date(2026, 7, 13, 12, 0, 0, 0, time.UTC)
	latest := api.CodeScan{
		ID:         "scan-run",
		Status:     api.CodeScanStatusRunning,
		Categories: []api.ScanCategory{api.ScanCategorySecurity},
		CreatedAt:  now,
	}
	builder := &SnapshotBuilder{
		Repo:  repotest.NewProvider(t),
		Scans: &stubScanSource{scans: []api.CodeScan{latest}},
		ScanCompare: &stubScanComparer{
			compare: &scan.Comparison{NewCount: 1},
		},
		SecurityScanners: testSecurityScannersStore(t, true),
	}
	snap, err := builder.Build(context.Background(), testdbseed.DefaultProjectID, t.TempDir(), "sess-1", api.BoardDetailLevelCompact, nil)
	testutil.FailErr(t, "Build", err)
	if snap.Scans == nil || snap.Scans.Compare != nil {
		t.Fatalf("running scan should omit compare: %+v", snap.Scans)
	}
}

func TestSnapshotBuilderBuildEnrichesActiveReservations(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	queue := worker.NewInMemoryQueue(10)
	task := api.WorkerTask{
		Prompt: "fixture",
		Brief:  "fixture",
		ID:     "job-a", ParentSessionID: "sess-1", ProjectID: testdbseed.DefaultProjectID, WorkspacePath: dir,
		AgentType: "implementer", Status: api.WorkerStatusRunning,
	}
	testutil.FailErr(t, "enqueue", worker.ApplyEnqueueDefaults(&task, project.ProjectScope{ProjectID: testdbseed.DefaultProjectID, WorkspacePath: dir}, worker.DefaultWorkersConfig()))
	_, err := queue.Enqueue(ctx, task)
	testutil.FailErr(t, "enqueue", err)

	b := &SnapshotBuilder{
		Workers: queue,
		Repo:    repotest.NewProvider(t),
		ActiveReservations: func(sessionID string) []api.BoardReservationEntry {
			if sessionID != "sess-1" {
				return nil
			}
			return []api.BoardReservationEntry{{
				Path:  "pkg/foo.go",
				JobID: "job-a",
			}}
		},
	}
	snap, err := b.Build(ctx, testdbseed.DefaultProjectID, dir, "sess-1", api.BoardDetailLevelCompact, nil)
	testutil.FailErr(t, "Build", err)
	view := BuildBoardView(snap, "sess-1", api.BoardDetailLevelCompact, time.Now().UTC())
	if len(view.Roster) != 1 {
		t.Fatalf("roster = %d want 1", len(view.Roster))
	}
	if got := view.Roster[0].Reservations; len(got) != 1 || got[0] != "pkg/foo.go" {
		t.Fatalf("reservations = %#v", got)
	}
}
