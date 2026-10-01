package extensionstate_test

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/catalogview"
	"github.com/lycaon/lycaon/internal/configlayout"
	"github.com/lycaon/lycaon/internal/extensionstate"
	"github.com/lycaon/lycaon/internal/extpacks"
	"github.com/lycaon/lycaon/internal/testutil"
)

type checkpointFailingJournal struct {
	finished int
}

func (*checkpointFailingJournal) Begin(context.Context, extensionstate.Operation) (string, error) {
	return "operation", nil
}

func (*checkpointFailingJournal) FilesApplied(context.Context, string) error {
	return errors.New("checkpoint unavailable")
}

func (j *checkpointFailingJournal) Finish(context.Context, string) error {
	j.finished++
	return nil
}

func (*checkpointFailingJournal) Recover(
	context.Context,
	extensionstate.Publisher,
	extensionstate.Emitter,
) error {
	return nil
}

func testOwner(t *testing.T) *extensionstate.Owner {
	t.Helper()
	t.Setenv("LYCAON_CONFIG_DIR", t.TempDir())
	root := configlayout.FindModuleRoot()
	if root == "" {
		t.Fatal("module root not found")
	}
	return &extensionstate.Owner{
		Views: catalogview.NewCache(root, slog.New(slog.NewTextHandler(io.Discard, nil))),
	}
}

func currentRevision(t *testing.T, owner *extensionstate.Owner, projectDir string) string {
	t.Helper()
	revision, err := owner.CurrentRevision(projectDir)
	testutil.FailErr(t, "current revision", err)
	return revision
}

func apply(t *testing.T, owner *extensionstate.Owner, scope extensionstate.Scope, op extensionstate.Op) extensionstate.Result {
	t.Helper()
	dir := ""
	if scope.Kind == "project" {
		dir = scope.ProjectDir
	}
	result, err := owner.Apply(t.Context(), extensionstate.Intent{
		Scope:            scope,
		ExpectedRevision: currentRevision(t, owner, dir),
		Op:               op,
	})
	testutil.FailErr(t, "apply intent", err)
	return result
}

func writeAuthorPack(t *testing.T, id string, files map[string]string) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "author")
	manifest := "manifest_version: 1\nid: " + id + "\nname: " + id +
		"\nversion: 1.0.0\ncompatibility:\n  extension_api: ^1.0.0\n"
	testutil.FailErr(t, "mkdir author pack", os.MkdirAll(dir, 0o755))
	testutil.FailErr(t, "write manifest", os.WriteFile(filepath.Join(dir, "extension.yaml"), []byte(manifest), 0o644))
	for rel, body := range files {
		path := filepath.Join(dir, filepath.FromSlash(rel))
		testutil.FailErr(t, "mkdir unit dir", os.MkdirAll(filepath.Dir(path), 0o755))
		testutil.FailErr(t, "write unit", os.WriteFile(path, []byte(body), 0o644))
	}
	return dir
}

var sha256Hex = regexp.MustCompile(`^[0-9a-f]{64}$`)

func TestRevisionIsOpaqueAndCanonical(t *testing.T) {
	owner := testOwner(t)
	first := currentRevision(t, owner, "")
	if !sha256Hex.MatchString(first) {
		t.Fatalf("revision %q is not a SHA-256 hex digest", first)
	}

	// Formatting-only differences hash to the same canonical state.
	path, err := extpacks.DeviceDesiredPath()
	testutil.FailErr(t, "device desired path", err)
	testutil.FailErr(t, "mkdir", os.MkdirAll(filepath.Dir(path), 0o700))
	testutil.FailErr(t, "write desired", os.WriteFile(path,
		[]byte("format: 1\npacks: []\ndisabled: []\nown: {}\n"), 0o600))
	formatted := currentRevision(t, owner, "")
	testutil.FailErr(t, "write desired reordered",
		os.WriteFile(path, []byte("own: {}\ndisabled: []\npacks: []\nformat: 1\n"), 0o600))
	if got := currentRevision(t, owner, ""); got != formatted {
		t.Fatalf("reordered file changed revision: %s vs %s", got, formatted)
	}
}

func TestApplyRequiresExpectedRevision(t *testing.T) {
	owner := testOwner(t)
	_, err := owner.Apply(t.Context(), extensionstate.Intent{
		Scope: extensionstate.Scope{Kind: "device"},
		Op:    extensionstate.SetUnitDisabledOp{UnitID: stockUnitIDAt(t, 0), Disabled: true},
	})
	if !errors.Is(err, extensionstate.ErrExpectedRevisionRequired) {
		t.Fatalf("err = %v, want ErrExpectedRevisionRequired", err)
	}
}

func TestApplyStaleRevisionCommitsNothing(t *testing.T) {
	owner := testOwner(t)
	stale := currentRevision(t, owner, "")
	apply(t, owner, extensionstate.Scope{Kind: "device"},
		extensionstate.SetUnitDisabledOp{UnitID: stockUnitIDAt(t, 0), Disabled: true})

	_, err := owner.Apply(t.Context(), extensionstate.Intent{
		Scope:            extensionstate.Scope{Kind: "device"},
		ExpectedRevision: stale,
		Op:               extensionstate.SetUnitDisabledOp{UnitID: "guidance/other-note", Disabled: true},
	})
	var staleErr *extensionstate.StaleError
	if !errors.As(err, &staleErr) {
		t.Fatalf("err = %v, want StaleError", err)
	}
	if staleErr.Current == "" || staleErr.Current == stale {
		t.Fatalf("stale result must carry the current revision, got %q", staleErr.Current)
	}
	path, err := extpacks.DeviceDesiredPath()
	testutil.FailErr(t, "device desired path", err)
	desired, err := extpacks.LoadDesiredFile(path)
	testutil.FailErr(t, "load desired", err)
	for _, id := range desired.Disabled {
		if id == "guidance/other-note" {
			t.Fatal("stale mutation must commit nothing")
		}
	}
}

func TestDesiredMutationCommitsAndMovesRevision(t *testing.T) {
	owner := testOwner(t)
	before := currentRevision(t, owner, "")
	result := apply(t, owner, extensionstate.Scope{Kind: "device"},
		extensionstate.SetUnitDisabledOp{UnitID: stockUnitIDAt(t, 0), Disabled: true})
	if result.Revision == "" || result.Revision == before {
		t.Fatalf("committed revision must move: %q vs %q", result.Revision, before)
	}
	found := false
	for _, id := range result.Desired.Disabled {
		found = found || id == stockUnitIDAt(t, 0)
	}
	if !found {
		t.Fatal("committed desired must carry the mutation")
	}

	enable := apply(t, owner, extensionstate.Scope{Kind: "device"},
		extensionstate.SetUnitDisabledOp{UnitID: stockUnitIDAt(t, 0), Disabled: false})
	if len(enable.Desired.Disabled) != 0 {
		t.Fatalf("re-enable must clear disabled, got %v", enable.Desired.Disabled)
	}
}

func TestCommittedProjectMutationReportsJournalCheckpointAsWarning(t *testing.T) {
	owner := testOwner(t)
	journal := &checkpointFailingJournal{}
	owner.Journal = journal
	projectDir := t.TempDir()
	result := apply(t, owner, projectScope(projectDir),
		extensionstate.SetUnitDisabledOp{UnitID: stockUnitIDAt(t, 0), Disabled: true})
	if len(result.Warnings) != 1 || !strings.Contains(result.Warnings[0], "checkpoint unavailable") {
		t.Fatalf("warnings = %v", result.Warnings)
	}
	if journal.finished != 0 {
		t.Fatal("an operation with an unrecorded files-applied checkpoint must stay recoverable")
	}
	desiredPath := extpacks.ProjectDesiredPath(projectDir)
	sug, err := extpacks.LoadSuggestionFile(desiredPath)
	testutil.FailErr(t, "load committed project suggestion", err)
	if len(sug.Disabled) != 1 || sug.Disabled[0] != stockUnitIDAt(t, 0) {
		t.Fatalf("committed suggestion = %+v", sug)
	}
}

func TestConfigurationCommitsUnderOneRevision(t *testing.T) {
	owner := testOwner(t)
	result := apply(t, owner, extensionstate.Scope{Kind: "device"}, extensionstate.SetConfigurationOp{
		Packs: map[string]map[string]any{
			"acme/reviewer": {"review_depth": "normal", "include_tests": true},
		},
	})
	got := result.Desired.Configuration["acme/reviewer"]
	if got["review_depth"] != "normal" || got["include_tests"] != true {
		t.Fatalf("configuration = %+v", got)
	}

	cleared := apply(t, owner, extensionstate.Scope{Kind: "device"},
		extensionstate.SetConfigurationOp{Packs: map[string]map[string]any{"acme/reviewer": {}}})
	if len(cleared.Desired.Configuration) != 0 {
		t.Fatalf("clearing must drop the pack block, got %+v", cleared.Desired.Configuration)
	}
}

func TestConfigurationRejectsNestedValues(t *testing.T) {
	owner := testOwner(t)
	_, err := owner.Apply(t.Context(), extensionstate.Intent{
		Scope:            extensionstate.Scope{Kind: "device"},
		ExpectedRevision: currentRevision(t, owner, ""),
		Op: extensionstate.SetConfigurationOp{
			Packs: map[string]map[string]any{
				"acme/valid":    {"enabled": true},
				"acme/reviewer": {"nested": map[string]any{"no": true}},
			},
		},
	})
	if err == nil {
		t.Fatal("nested configuration values must reject the candidate")
	}
	desiredPath, pathErr := extpacks.DeviceDesiredPath()
	testutil.FailErr(t, "device desired path", pathErr)
	desired, loadErr := extpacks.LoadDesiredFile(desiredPath)
	if loadErr != nil && !os.IsNotExist(loadErr) {
		testutil.FailErr(t, "load rejected configuration", loadErr)
	}
	if desired.Configuration["acme/valid"] != nil {
		t.Fatalf("valid sibling block committed from rejected save: %+v", desired.Configuration)
	}
}

func TestConfigurationRejectsEmptyReplacementSet(t *testing.T) {
	owner := testOwner(t)
	_, err := owner.Apply(t.Context(), extensionstate.Intent{
		Scope:            extensionstate.Scope{Kind: "device"},
		ExpectedRevision: currentRevision(t, owner, ""),
		Op:               extensionstate.SetConfigurationOp{Packs: map[string]map[string]any{}},
	})
	if err == nil || !strings.Contains(err.Error(), "at least one pack block") {
		t.Fatalf("err = %v", err)
	}
}

func TestInstallLinkedDevelopmentPack(t *testing.T) {
	owner := testOwner(t)
	author := writeAuthorPack(t, "acme/devpack", map[string]string{
		"guidance/dev-note.md": "note\n",
	})
	result := apply(t, owner, extensionstate.Scope{Kind: "device"},
		extensionstate.InstallOp{Source: "path:" + author})
	if result.Install == nil || result.Install.PackID != "acme/devpack" || result.Install.Kind != extpacks.PackKindPath {
		t.Fatalf("install result = %+v", result.Install)
	}
	row, ok := extpacks.DesiredPackRow(result.Desired, "acme/devpack")
	if !ok || !row.Development {
		t.Fatalf("desired row = %+v ok=%v", row, ok)
	}
	lockPath, err := extpacks.DeviceLockPath()
	testutil.FailErr(t, "device lock path", err)
	lock, err := extpacks.LoadLockFile(lockPath)
	testutil.FailErr(t, "load lock", err)
	if _, ok := lock.Package("acme/devpack"); !ok {
		t.Fatal("lock must carry the linked package")
	}

	content, err := extpacks.DiscoverAllContent(nil)
	testutil.FailErr(t, "discover", err)
	eff := extpacks.Resolve(t.Context(), extpacks.ResolveInput{
		Packs: content, Desired: result.Desired,
	})
	if !eff.HasLoaded("guidance/dev-note") {
		t.Fatal("installed pack unit must resolve")
	}
}

func TestCandidateRejectionLeavesStateUntouched(t *testing.T) {
	owner := testOwner(t)
	author := writeAuthorPack(t, "acme/badpack", map[string]string{
		"contributions/commands/steal.yaml": "id: acme/other:steal\n",
	})
	before := currentRevision(t, owner, "")
	_, err := owner.Apply(t.Context(), extensionstate.Intent{
		Scope:            extensionstate.Scope{Kind: "device"},
		ExpectedRevision: before,
		Op:               extensionstate.InstallOp{Source: "path:" + author},
	})
	if err == nil {
		t.Fatal("candidate with a foreign-namespace contribution must reject")
	}
	if got := currentRevision(t, owner, ""); got != before {
		t.Fatalf("rejection must leave live state untouched: %s vs %s", got, before)
	}
	path, err := extpacks.DeviceDesiredPath()
	testutil.FailErr(t, "device desired path", err)
	if _, statErr := os.Stat(path); !os.IsNotExist(statErr) {
		desired, loadErr := extpacks.LoadDesiredFile(path)
		testutil.FailErr(t, "load desired", loadErr)
		if _, ok := extpacks.DesiredPackRow(desired, "acme/badpack"); ok {
			t.Fatal("rejected pack must not reach desired state")
		}
	}
}

// A disabled schema removes custom copy without removing the tool.
func TestDisablingStockToolSchemaUnitAcceptsCandidate(t *testing.T) {
	owner := testOwner(t)
	before := currentRevision(t, owner, "")
	res, err := owner.Apply(t.Context(), extensionstate.Intent{
		Scope:            extensionstate.Scope{Kind: "device"},
		ExpectedRevision: before,
		Op:               extensionstate.SetUnitDisabledOp{UnitID: "tools/schemas/write", Disabled: true},
	})
	testutil.FailErr(t, "disable stock tool schema unit", err)
	if res.Revision == before {
		t.Fatal("accepted disable must move the revision")
	}
	path, pathErr := extpacks.DeviceDesiredPath()
	testutil.FailErr(t, "device desired path", pathErr)
	desired, loadErr := extpacks.LoadDesiredFile(path)
	testutil.FailErr(t, "load desired", loadErr)
	found := false
	for _, id := range desired.Disabled {
		if id == "tools/schemas/write" {
			found = true
		}
	}
	if !found {
		t.Fatal("accepted disable must reach desired state")
	}
}

func TestDisablingPlatformRejectsCandidate(t *testing.T) {
	owner := testOwner(t)
	before := currentRevision(t, owner, "")
	_, err := owner.Apply(t.Context(), extensionstate.Intent{
		Scope:            extensionstate.Scope{Kind: "device"},
		ExpectedRevision: before,
		Op:               extensionstate.SetPackEnabledOp{PackID: "painted-wolf/platform", Enabled: false},
	})
	if err == nil {
		t.Fatal("a candidate that cannot boot must reject")
	}
	if got := currentRevision(t, owner, ""); got != before {
		t.Fatal("rejection must not move the revision")
	}
}

func TestRemoveInstalledPack(t *testing.T) {
	owner := testOwner(t)
	author := writeAuthorPack(t, "acme/devpack", map[string]string{
		"guidance/dev-note.md": "note\n",
	})
	apply(t, owner, extensionstate.Scope{Kind: "device"},
		extensionstate.InstallOp{Source: "path:" + author})
	result := apply(t, owner, extensionstate.Scope{Kind: "device"},
		extensionstate.RemoveOp{PackID: "acme/devpack"})
	if _, ok := extpacks.DesiredPackRow(result.Desired, "acme/devpack"); ok {
		t.Fatal("removed pack must leave desired state")
	}
	lockPath, err := extpacks.DeviceLockPath()
	testutil.FailErr(t, "device lock path", err)
	lock, err := extpacks.LoadLockFile(lockPath)
	testutil.FailErr(t, "load lock", err)
	if _, ok := lock.Package("acme/devpack"); ok {
		t.Fatal("removed pack must leave the lock")
	}
}

func TestProjectScopeMutationCommitsProjectFile(t *testing.T) {
	owner := testOwner(t)
	projectDir := t.TempDir()
	scope := extensionstate.Scope{Kind: "project", ProjectID: "proj-1", ProjectDir: projectDir}
	result := apply(t, owner, scope,
		extensionstate.SetUnitDisabledOp{UnitID: stockUnitIDAt(t, 0), Disabled: true})
	if result.DesiredPath != extpacks.ProjectDesiredPath(projectDir) {
		t.Fatalf("desired path = %s", result.DesiredPath)
	}
	if _, err := os.Stat(result.DesiredPath); err != nil {
		t.Fatalf("project desired file missing: %v", err)
	}

	devicePath, err := extpacks.DeviceDesiredPath()
	testutil.FailErr(t, "device desired path", err)
	if _, err := os.Stat(devicePath); !os.IsNotExist(err) {
		device, loadErr := extpacks.LoadDesiredFile(devicePath)
		testutil.FailErr(t, "load device desired", loadErr)
		if len(device.Disabled) != 0 {
			t.Fatal("project mutation must not touch device state")
		}
	}
}

func TestOutOfBandWriteFailsCommitClosed(t *testing.T) {
	owner := testOwner(t)
	// The committed bytes establish the CAS baseline.
	apply(t, owner, extensionstate.Scope{Kind: "device"},
		extensionstate.SetUnitDisabledOp{UnitID: stockUnitIDAt(t, 0), Disabled: true})

	path, err := extpacks.DeviceDesiredPath()
	testutil.FailErr(t, "device desired path", err)
	revision := currentRevision(t, owner, "")

	// A hand edit after the read makes the commit stale.
	testutil.FailErr(t, "hand edit", os.WriteFile(path,
		[]byte("format: 1\npacks: []\ndisabled: [guidance/hand-edit]\nown: {}\n"), 0o600))
	_, err = owner.Apply(context.Background(), extensionstate.Intent{
		Scope:            extensionstate.Scope{Kind: "device"},
		ExpectedRevision: revision,
		Op:               extensionstate.SetUnitDisabledOp{UnitID: "guidance/other-note", Disabled: true},
	})
	var staleErr *extensionstate.StaleError
	if !errors.As(err, &staleErr) {
		t.Fatalf("err = %v, want StaleError", err)
	}
}
