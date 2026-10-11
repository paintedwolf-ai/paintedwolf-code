package workflows

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/authzcontext"
	"github.com/lycaon/lycaon/internal/delegation"
	"github.com/lycaon/lycaon/internal/evidence"
	"github.com/lycaon/lycaon/internal/project"
	"github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/internal/testdbfixture"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/webresearch"
	"github.com/lycaon/lycaon/pkg/api"
	"slices"
	"strings"
)

type evidenceBindingFixture struct {
	runtime *Runtime
	deps    Dependencies
	runID   string
}

func buildEvidenceFixture(t *testing.T) evidenceBindingFixture {
	t.Helper()
	database := testdbfixture.Open(t, "workflow-evidence.db")
	testdbseed.InsertSessionWithRoot(t, database, "session", testdbseed.DefaultProjectID, t.TempDir())
	deps := Dependencies{
		Database: database, DataDir: t.TempDir(), Sessions: store.NewSQL(database),
		DelegationStore: delegation.NewSQLStore(database), AuthzRecorder: authzcontext.SQLRecorder(database),
	}
	run, err := deps.DelegationStore.Create(t.Context(), api.Delegation{
		ProjectID: testdbseed.DefaultProjectID, WorkspacePath: t.TempDir(), Task: "verify", Strategy: api.HuntStrategyFileBased,
	}, "session", []api.Leg{{Title: "verify"}})
	testutil.FailErr(t, "create evidence delegation", err)
	runtime, err := Build(t.Context(), deps, nil)
	testutil.FailErr(t, "build evidence runtime", err)
	return evidenceBindingFixture{runtime: runtime, deps: deps, runID: run.ID}
}

func TestBuildEvidenceSharesProjectHistoryAndSearchAttribution(t *testing.T) {
	f := buildEvidenceFixture(t)
	record := evidence.GateRecord(evidence.GateTypeOptionsJudge, "judge", f.runID,
		evidence.GateVerdictApproved, "selected option", map[string]any{"winner": "B"}, "reviewer", "", "", 1, time.Now().UTC())
	testutil.FailErr(t, "record inspector evidence", f.runtime.Inspector.RecordEvidence(t.Context(), f.runID, "judge", record))
	rows, err := f.runtime.Manager.Verdicts.ListReviewLoopEvidenceForType(t.Context(), "session", f.runID, "judge", evidence.GateTypeOptionsJudge)
	testutil.FailErr(t, "read inspector record through verdict service", err)
	if len(rows) != 1 || rows[0].Summary != record.Summary || rows[0].Artifacts["winner"] != "B" {
		t.Fatalf("verdict service did not read inspector's durable record: %+v", rows)
	}
	projectDir := project.HostDataDir(f.deps.DataDir, testdbseed.DefaultProjectID)
	retained, err := f.runtime.Evidence.ReadAll(t.Context(), projectDir, f.runID, "judge", evidence.GateTypeOptionsJudge)
	testutil.FailErr(t, "read retained project evidence", err)
	if len(retained) != 1 || retained[0].Summary != record.Summary {
		t.Fatalf("runtime evidence belongs to another project or store: %+v", retained)
	}
	for range 2 {
		f.runtime.Manager.Verdicts.OnGateEvidencePersisted(t.Context(), "session", f.runID, record)
	}
	f.runtime.Manager.Verdicts.OnGateEvidencePersisted(t.Context(), "absent", f.runID, record)
	var count int
	testutil.FailErr(t, "read attributed search projection", f.deps.Database.QueryRowContext(t.Context(), `
		SELECT COUNT(*) FROM evidence_index WHERE project_id = ? AND session_id = ?
		AND workflow_run_id = ? AND verdict = ? AND snippet LIKE ?`,
		testdbseed.DefaultProjectID, "session", f.runID, string(evidence.GateVerdictApproved), "%winner: B%").Scan(&count))
	if count != 1 {
		t.Fatalf("durable evidence projection count = %d, want one attributed row", count)
	}
}

func TestBuildEvidenceResolutionFailsBeforePublishing(t *testing.T) {
	f := buildEvidenceFixture(t)
	if _, err := f.runtime.Inspector.ProjectDir(t.Context(), "absent"); !errors.Is(err, delegation.ErrDelegationNotFound) {
		t.Fatalf("missing delegation resolution = %v", err)
	}
	if _, err := f.runtime.Manager.Verdicts.EvidenceProjectDir(t.Context(), "absent"); !errors.Is(err, store.ErrSessionNotFound) {
		t.Fatalf("missing session resolution = %v", err)
	}
	canceled, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := f.runtime.Inspector.ProjectDir(canceled, f.runID); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled delegation resolution = %v", err)
	}
	if _, err := f.runtime.Manager.Verdicts.EvidenceProjectDir(canceled, "session"); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled session resolution = %v", err)
	}
	blocked := filepath.Join(t.TempDir(), "host-data")
	testutil.FailErr(t, "block host data directory", os.WriteFile(blocked, []byte("occupied"), 0o600))
	f.deps.DataDir = blocked
	runtime, err := Build(t.Context(), f.deps, nil)
	testutil.FailErr(t, "build blocked evidence runtime", err)
	if err := runtime.Inspector.RecordEvidence(t.Context(), f.runID, "judge", evidence.Record{GateType: string(evidence.GateTypeOptionsJudge)}); err == nil {
		t.Fatal("inspector published evidence without a project directory")
	}
	if _, err := runtime.Manager.Verdicts.EvidenceProjectDir(t.Context(), "session"); err == nil {
		t.Fatal("verdict evidence ignored the blocked project directory")
	}
	f.deps.Sessions, f.deps.DelegationStore = nil, nil
	runtime, err = Build(t.Context(), f.deps, nil)
	testutil.FailErr(t, "build runtime without project lookup services", err)
	if dir, err := runtime.Inspector.ProjectDir(t.Context(), f.runID); dir != "" || err != nil {
		t.Fatalf("absent delegation lookup = %q, %v", dir, err)
	}
	if dir, err := runtime.Manager.Verdicts.EvidenceProjectDir(t.Context(), "session"); dir != "" || err != nil {
		t.Fatalf("absent session lookup = %q, %v", dir, err)
	}
}

func TestWorkflowRuntimeBlueprintsUseRegisteredRootAndReviewRosterRespectsWebPreference(t *testing.T) {
	f := buildEvidenceFixture(t)
	f.deps.Projects = project.NewSQLRegistry(f.deps.Database)
	webPrefs := webresearch.NewConfigStoreAt(filepath.Join(t.TempDir(), "web-research.yaml"))
	off := false
	testutil.FailErr(t, "disable external search", webPrefs.ApplyPrefs(nil, nil, &off, nil))
	f.deps.WebResearchConfig = webPrefs
	runtime, err := Build(t.Context(), f.deps, nil)
	testutil.FailErr(t, "build blueprint runtime", err)
	blueprint, err := runtime.Blueprints.Create(t.Context(), testdbseed.DefaultProjectID, "Retained review plan", "", "", "explicit plan content")
	testutil.FailErr(t, "create registered-project blueprint", err)
	read, err := runtime.Blueprints.Get(t.Context(), testdbseed.DefaultProjectID, blueprint.Path)
	testutil.FailErr(t, "read registered-project blueprint", err)
	if !strings.HasSuffix(strings.TrimSpace(read.Content), "explicit plan content") || read.ProjectID != testdbseed.DefaultProjectID || read.Status != api.BlueprintStatusDraft {
		t.Fatalf("blueprint content crossed project root:%+v", read)
	}
	registeredProject, err := f.deps.Projects.Get(t.Context(), testdbseed.DefaultProjectID)
	testutil.FailErr(t, "resolve registered blueprint destination", err)
	retained, err := os.ReadFile(filepath.Join(project.PrimaryRootPath(registeredProject), filepath.FromSlash(read.Path)))
	testutil.FailErr(t, "read retained blueprint from registered root", err)
	if string(retained) != read.Content {
		t.Fatal("blueprint read diverged from its registered-project file")
	}
	if _, err := runtime.Blueprints.Create(t.Context(), "missing-project", "Rejected plan", "", "", "body"); err == nil {
		t.Fatal("blueprint persisted without a registered project root")
	}
	candidates := []string{"implementer", "web-researcher"}
	restricted := runtime.Manager.Phases.ReviewSpawnFilter(t.Context(), "session", "phase", candidates)
	if !slices.Contains(restricted, "implementer") || slices.Contains(restricted, "web-researcher") {
		t.Fatalf("web preference removed local reviewer:%v", restricted)
	}
	on := true
	testutil.FailErr(t, "enable external search", webPrefs.ApplyPrefs(nil, nil, &on, nil))
	restored := runtime.Manager.Phases.ReviewSpawnFilter(t.Context(), "session", "phase", candidates)
	if !slices.Equal(restored, candidates) {
		t.Fatalf("enabled web search did not restore review candidates:%v", restored)
	}
}
