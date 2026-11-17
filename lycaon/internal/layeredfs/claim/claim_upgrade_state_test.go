package claim

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"slices"
	"testing"

	"github.com/lycaon/lycaon/internal/layeredfs/adapter"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/testutil/gittest"
)

type versionProbe struct {
	FormatVersion *int `json:"format_version"`
}

func LoadViewMetadata(path string) (ViewMetadataV2, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return ViewMetadataV2{}, err
	}
	var vp versionProbe
	if err := json.Unmarshal(raw, &vp); err != nil || vp.FormatVersion == nil {
		return ViewMetadataV2{}, fmt.Errorf("%w: missing or unreadable format_version", ErrInvalidMetadata)
	}
	if *vp.FormatVersion != FormatVersionV2 {
		return ViewMetadataV2{}, fmt.Errorf("%w: version %d", ErrUnsupportedMetadataVersion, *vp.FormatVersion)
	}
	var meta ViewMetadataV2
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&meta); err != nil {
		return ViewMetadataV2{}, fmt.Errorf("%w: %w", ErrInvalidMetadata, err)
	}
	if err := validateViewMetadataV2(meta); err != nil {
		return ViewMetadataV2{}, err
	}
	return meta, nil
}

func TestViewMetadataRoundTripsAndRefusesUnknownShapesWithoutWrites(t *testing.T) {
	t.Parallel()
	tmp := t.TempDir()
	meta := ViewMetadataV2{
		Identity: ClaimIdentity{WorkerID: "job-42", ProjectID: "proj", ViewID: "view-42", SourceGeneration: 19},
		SelectedRoots: []RootMappingRecord{{
			RootID: "primary", Label: "primary", SourcePath: filepath.Join(tmp, "trunk"),
			WorkspacePath: filepath.Join(tmp, "branch", "primary"), IsPrimary: true,
		}},
		Adapter: AdapterMetadataFacts{
			AdapterID: adapter.AdapterEagerCopy, PreparationKind: adapter.PreparationMaterialized,
			SourceVisibility: adapter.VisibilityFixed,
		},
		JournalPath: filepath.Join(tmp, "branch", "journal"),
		Checkpoints: CheckpointMetadataRefs{LastPublishedCheckpointID: "cp-1"},
	}
	path := filepath.Join(tmp, "view-metadata.json")
	testutil.FailErr(t, "SaveViewMetadata", SaveViewMetadata(path, meta))
	loaded, err := LoadViewMetadata(path)
	testutil.FailErr(t, "LoadViewMetadata", err)
	if loaded.FormatVersion != FormatVersionV2 || loaded.Identity != meta.Identity || loaded.Adapter.AdapterID != adapter.AdapterEagerCopy ||
		loaded.Checkpoints.LastPublishedCheckpointID != "cp-1" {
		t.Fatalf("loaded metadata = %+v, want the saved record", loaded)
	}

	for _, tc := range []struct {
		name    string
		payload string
		wantErr error
	}{
		{"v0", `{"format_version": 0, "job_id": "x"}`, ErrUnsupportedMetadataVersion},
		{"v1", `{"format_version": 1, "job_id": "x", "roots": []}`, ErrUnsupportedMetadataVersion},
		{"v3", `{"format_version": 3, "identity": {"worker_id": "w", "view_id": "v"}}`, ErrUnsupportedMetadataVersion},
		{"unknown_field", `{"format_version": 2, "unknown_shape": true}`, ErrInvalidMetadata},
	} {
		badPath := filepath.Join(tmp, "bad-"+tc.name+".json")
		testutil.FailErr(t, "write "+tc.name, os.WriteFile(badPath, []byte(tc.payload), 0o600))
		if _, lErr := LoadViewMetadata(badPath); !errors.Is(lErr, tc.wantErr) {
			t.Fatalf("%s: expected %v, got %v", tc.name, tc.wantErr, lErr)
		}
		afterBytes, rErr := os.ReadFile(badPath)
		testutil.FailErr(t, "read after "+tc.name, rErr)
		if string(afterBytes) != tc.payload {
			t.Fatalf("%s: refused metadata file was mutated on disk: got %q, want %q", tc.name, string(afterBytes), tc.payload)
		}
	}
}

func TestHostAndDenWorkspaceStateAndReceiptsDistinguishGitCommandAndCheckpoint(t *testing.T) {
	t.Parallel()
	tmp := t.TempDir()
	trunk := filepath.Join(tmp, "trunk")
	testutil.FailErr(t, "mkdir trunk", os.MkdirAll(trunk, 0o755))
	gittest.Init(t, trunk)
	testutil.FailErr(t, "write file", os.WriteFile(filepath.Join(trunk, "hello.txt"), []byte("hello\n"), 0o644))
	gittest.CommitAll(t, trunk, "initial commit")

	// A workspace under the preparation limit materializes without consulting a mount.
	unavailableMount := adapter.NewMountedAdapter(adapter.MountedAdapterConfig{
		AdapterID: adapter.AdapterFUSELinux,
		TargetOS:  "linux",
		MountProbeFn: func(_ context.Context, _, _ string) (adapter.ReasonCode, string, error) {
			return adapter.ReasonFUSEDeviceUnavailable, "/dev/fuse missing", errors.New("no fuse")
		},
	})
	coord := NewCoordinator(adapter.NewSelector("linux", unavailableMount, unavailableMount, adapter.NewEagerCopyAdapter()))

	cv, snap, err := coord.ClaimWorkerView(t.Context(), ClaimRequest{
		Identity:       ClaimIdentity{WorkerID: "w-mat", ProjectID: "proj-mat", ViewID: "v-mat", SourceGeneration: 3},
		Storage:        ClaimStorage{RuntimeDir: filepath.Join(tmp, "rt")},
		Grants:         []adapter.RootGrant{{RootID: "primary", Label: "primary", SourcePath: trunk, IsPrimary: true}},
		FocusPathHints: []string{"hello.txt"},
	})
	testutil.FailErr(t, "ClaimWorkerView materialized", err)
	defer func() { _ = coord.ReleaseWorkerView(t.Context(), "v-mat", nil) }()

	// 1. A materialized workspace is ready with its private Git initialized and its copy counted.
	receipt := BuildReceipt(snap)
	if receipt.AdapterID != adapter.AdapterEagerCopy ||
		receipt.Readiness != StateReady ||
		receipt.GitInit != GitInitInitialized ||
		snap.Adapter.SelectionReason != adapter.SelectionUnderBudget ||
		receipt.CopiedEntries == 0 || receipt.CopiedBytes == 0 {
		t.Fatalf("expected a ready materialized receipt with initialized git, got %+v", receipt)
	}
	if len(cv.Metadata.Adapter.SeededGitRepos) == 0 {
		t.Fatalf("expected materialized claim to discover and initialize git repo during seed, got %+v", cv.Metadata.Adapter)
	}

	// 2. Live background command (CommandRunning) never makes a ready workspace appear unready.
	snapCmd, err := coord.SetCommandState("v-mat", CommandRunning)
	testutil.FailErr(t, "SetCommandState running", err)
	if snapCmd.Command != CommandRunning || snapCmd.Readiness != StateReady || snapCmd.Checkpoint.State != CheckpointNone {
		t.Fatalf("command running must keep readiness=ready and checkpoint distinct, got %+v", snapCmd)
	}

	// 3. Failed checkpoint persistence surfaces CheckpointFailedPersistence + ActionRetryCheckpoint while
	// Readiness stays StateReady.
	snapCPFail, err := coord.RecordCheckpointFailure("v-mat", "cp-attempt-1", adapter.ReasonBarrierFailed, "disk full writing checkpoint")
	testutil.FailErr(t, "RecordCheckpointFailure", err)
	recCPFail := BuildReceipt(snapCPFail)
	if recCPFail.Readiness != StateReady ||
		recCPFail.Command != CommandRunning ||
		recCPFail.Checkpoint.State != CheckpointFailedPersistence ||
		recCPFail.Notice.Severity != SeverityWarning ||
		!slices.Contains(recCPFail.SupportedActions, ActionRetryCheckpoint) {
		t.Fatalf("failed checkpoint must remain distinct from readiness/command and offer retry_checkpoint, got %+v", recCPFail)
	}

	// 4. Subsequent checkpoint publication succeeds and updates LatestCheckpointID.
	snapCPPub, err := coord.RecordCheckpointPublished("v-mat", "cp-attempt-1")
	testutil.FailErr(t, "RecordCheckpointPublished", err)
	if snapCPPub.Checkpoint.State != CheckpointPublished || snapCPPub.Checkpoint.LatestCheckpointID != "cp-attempt-1" {
		t.Fatalf("expected published checkpoint state, got %+v", snapCPPub.Checkpoint)
	}
}

func TestSmallerWorkspaceSelectionRetryAfterLimitAndFocusHintsDoNotChangeWorkspaceContent(t *testing.T) {
	t.Parallel()
	tmp := t.TempDir()
	rootSmall := filepath.Join(tmp, "root-small")
	rootDocs := filepath.Join(tmp, "root-docs")
	rootHuge := filepath.Join(tmp, "root-huge")
	for _, d := range []string{rootSmall, rootDocs, rootHuge} {
		testutil.FailErr(t, "mkdir", os.MkdirAll(d, 0o755))
	}
	testutil.FailErr(t, "write small", os.WriteFile(filepath.Join(rootSmall, "app.go"), []byte("package app\n"), 0o644))
	testutil.FailErr(t, "write docs", os.WriteFile(filepath.Join(rootDocs, "README.md"), []byte("# docs\n"), 0o644))
	for i := 0; i < 25; i++ {
		testutil.FailErr(t, "write huge", os.WriteFile(filepath.Join(rootHuge, fmt.Sprintf("blob_%02d.bin", i)), []byte("0123456789"), 0o644))
	}

	// A disk with room for the small selection only.
	disk := func() (adapter.DiskSpace, error) {
		return adapter.DiskSpace{Path: tmp, Available: 64 * 1024, Known: true}, nil
	}
	sel := adapter.NewSelector("unknown-os", nil, nil, adapter.NewEagerCopyAdapter())
	coord := NewCoordinator(sel)
	grants := []adapter.RootGrant{
		{RootID: "primary", Label: "primary", SourcePath: rootSmall, IsPrimary: true},
		{RootID: "docs", Label: "docs", SourcePath: rootDocs},
		{RootID: "huge", Label: "huge", SourcePath: rootHuge},
	}

	// 1. All 3 roots do not fit, which refuses with the number and offers a smaller selection.
	_, failSnap, err := coord.ClaimWorkerView(t.Context(), ClaimRequest{
		Identity:        ClaimIdentity{WorkerID: "w-limit", ProjectID: "p", ViewID: "v-limit"},
		Storage:         ClaimStorage{RuntimeDir: filepath.Join(tmp, "rt")},
		Grants:          grants,
		Materialization: ClaimMaterialization{Disk: disk},
	})
	var capErr *adapter.WorkspaceCapacityError
	if !errors.As(err, &capErr) || capErr.Available != 64*1024 || capErr.Required <= capErr.Available {
		t.Fatalf("expected a refusal with the numbers, got %v", err)
	}
	if failSnap.Readiness != StateFailedPreparation || failSnap.Notice.ReasonCode != adapter.ReasonWorkspaceDoesNotFit {
		t.Fatalf("expected StateFailedPreparation with ReasonWorkspaceDoesNotFit, got snap=%+v err=%v", failSnap, err)
	}
	if !slices.Contains(failSnap.SupportedActions, ActionSelectSmallerWorkspace) || !slices.Contains(failSnap.SupportedActions, ActionRetryClaim) {
		t.Fatalf("expected ActionSelectSmallerWorkspace and ActionRetryClaim, got %+v", failSnap.SupportedActions)
	}

	// 2. Caller selects a smaller permitted WorkspaceSelection (primary + docs only) and retries successfully.
	cv, okSnap, err := coord.ClaimWorkerView(t.Context(), ClaimRequest{
		Identity:        ClaimIdentity{WorkerID: "w-limit", ProjectID: "p", ViewID: "v-limit"},
		Storage:         ClaimStorage{RuntimeDir: filepath.Join(tmp, "rt")},
		Grants:          grants,
		Selection:       adapter.WorkspaceSelection{RootIDs: []string{"primary", "docs"}},
		FocusPathHints:  []string{"app.go"},
		Materialization: ClaimMaterialization{Disk: disk},
	})
	testutil.FailErr(t, "retry ClaimWorkerView with smaller selection", err)
	defer func() { _ = coord.ReleaseWorkerView(t.Context(), "v-limit", nil) }()

	if okSnap.Readiness != StateReady || len(okSnap.SelectedRoots) != 2 {
		t.Fatalf("expected ready 2-root workspace after retry, got %+v", okSnap)
	}
	rootsBefore := append([]RootMappingRecord(nil), okSnap.SelectedRoots...)

	// 3. Changing FocusPathHints alone never changes visible workspace roots or workspace content.
	updatedSnap, err := coord.UpdateFocusPathHints("v-limit", []string{"huge/blob_00.bin", "docs/README.md"})
	testutil.FailErr(t, "UpdateFocusPathHints", err)
	if !reflect.DeepEqual(updatedSnap.SelectedRoots, rootsBefore) {
		t.Fatalf("UpdateFocusPathHints must not alter SelectedRoots: before=%+v after=%+v", rootsBefore, updatedSnap.SelectedRoots)
	}
	if _, stErr := os.Stat(filepath.Join(cv.Prepared.RuntimeViewDir, "seeds", "huge")); !errors.Is(stErr, os.ErrNotExist) {
		t.Fatalf("changing focus hints must not materialize unselected root 'huge', got err=%v", stErr)
	}
}

func TestClaimInterruptedAtEachBoundaryLeavesNoReadyPartialWorkerOrLeakedRetention(t *testing.T) {
	t.Parallel()
	tmp := t.TempDir()
	src := filepath.Join(tmp, "src")
	testutil.FailErr(t, "mkdir src", os.MkdirAll(src, 0o755))
	testutil.FailErr(t, "write file", os.WriteFile(filepath.Join(src, "f.txt"), []byte("v1\n"), 0o644))

	coord := NewCoordinator(newMockMountedSelector(runtime.GOOS))
	ret := newTestRetentionRegistrar()
	grants := []adapter.RootGrant{{RootID: "primary", Label: "primary", SourcePath: src, IsPrimary: true}}

	_, _, err := coord.ClaimWorkerView(t.Context(), ClaimRequest{
		Identity: ClaimIdentity{WorkerID: "survivor", ProjectID: "p", ViewID: "view-survivor"},
		Storage:  ClaimStorage{RuntimeDir: filepath.Join(tmp, "rt")},
		Grants:   grants,
		Hooks:    ClaimHooks{Retention: ret, VerifyConfinement: passingConfinement},
	})
	testutil.FailErr(t, "survivor claim", err)
	defer func() { _ = coord.ReleaseWorkerView(context.Background(), "view-survivor", ret) }()

	stages := []ClaimStage{
		StageValidateSelection,
		StagePrepareAdapter,
		StageRegisterRetention,
		StageVerifyConfinement,
		StagePersistMetadata,
	}
	for _, stage := range stages {
		viewID := "view-fault-" + string(stage)
		metaPath := filepath.Join(tmp, "meta-"+string(stage)+".json")
		injected := errors.New("injected fault at " + string(stage))
		cv, snap, cErr := coord.ClaimWorkerView(t.Context(), ClaimRequest{
			Identity: ClaimIdentity{WorkerID: "w-" + string(stage), ProjectID: "p", ViewID: viewID},
			Storage:  ClaimStorage{RuntimeDir: filepath.Join(tmp, "rt"), MetadataPath: metaPath},
			Grants:   grants,
			Hooks: ClaimHooks{
				Retention:         ret,
				VerifyConfinement: passingConfinement,
				Fault: func(_ context.Context, s ClaimStage) error {
					if s == stage {
						return injected
					}
					return nil
				},
			},
		})
		if !errors.Is(cErr, injected) || cv != nil || snap.Readiness != StateFailedPreparation {
			t.Fatalf("stage %s: expected injected fault and StateFailedPreparation, got cv=%v snap=%+v err=%v", stage, cv, snap, cErr)
		}
		if ret.activeCountForView(viewID) != 0 {
			t.Fatalf("stage %s: leaked retention claim for %s", stage, viewID)
		}
		if _, stErr := os.Stat(metaPath); !errors.Is(stErr, os.ErrNotExist) {
			t.Fatalf("stage %s: expected no committed metadata file, got err=%v", stage, stErr)
		}
		if ret.activeCountForView("view-survivor") != 1 {
			t.Fatalf("stage %s: survivor retention claim was disturbed", stage)
		}
	}

	if _, _, cErr := coord.ClaimWorkerView(t.Context(), ClaimRequest{
		Identity: ClaimIdentity{WorkerID: "w-bad-conf", ProjectID: "p", ViewID: "view-bad-conf"},
		Storage:  ClaimStorage{RuntimeDir: filepath.Join(tmp, "rt")},
		Grants:   grants,
		Hooks: ClaimHooks{
			Retention: ret,
			VerifyConfinement: func(_ context.Context, _ *adapter.PreparedView) error {
				return errors.New("mount not usable inside sandbox")
			},
		},
	}); !errors.Is(cErr, ErrConfinementFailed) || ret.activeCountForView("view-bad-conf") != 0 {
		t.Fatalf("expected ErrConfinementFailed and 0 leaked retention claims, got %v", cErr)
	}
}
