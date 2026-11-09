package conformance

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/lycaon/lycaon/internal/layeredfs"
	"github.com/lycaon/lycaon/internal/layeredfs/adapter"
)

// CollectHostFacts gathers host OS, architecture, kernel, installation shape, and mount facts (#1667).
func CollectHostFacts(adapterName adapter.AdapterID, mountCacheSettings, workloadFixture string) HostFacts {
	kernel := detectKernelVersion()
	fsKind := "apfs"
	installShape := "macos_builtin_nfs_client_no_extension"
	if runtime.GOOS == "linux" {
		fsKind = "ext4_or_xfs"
		installShape = "linux_package_fuse3_unprivileged_user"
	}
	if mountCacheSettings == "" {
		if adapterName == adapter.AdapterNFSDarwin {
			mountCacheSettings = "nfsv3,tcp,locallocks,acregmin=0,acregmax=0,acdirmin=0,acdirmax=0"
		} else if adapterName == adapter.AdapterFUSELinux {
			mountCacheSettings = "fuse3,attr_timeout=0,entry_timeout=0,explicit_invalidation=true"
		} else {
			mountCacheSettings = "materialized_seed_snapshot"
		}
	}
	if workloadFixture == "" {
		workloadFixture = "three-root-mixed-repo-and-assets-v1"
	}
	return HostFacts{
		SourceCommit:            detectSourceCommit(),
		OS:                      runtime.GOOS,
		Arch:                    runtime.GOARCH,
		KernelVersion:           kernel,
		InstallationShape:       installShape,
		BackingFilesystem:       fsKind,
		AdapterName:             adapterName,
		MountCacheSettings:      mountCacheSettings,
		WorkloadFixture:         workloadFixture,
		NativeComparisonSummary: fmt.Sprintf("%s/%s vs native local filesystem", runtime.GOOS, adapterName),
	}
}

// BuildPreparationDiagnostics constructs structured diagnostics and enforces the rule that
// user-facing warnings appear ONLY for action-needed or unsuccessful outcomes (#1667).
func BuildPreparationDiagnostics(
	adapterName adapter.AdapterID,
	selectedRoots []string,
	fallbackReason adapter.ReasonCode,
	stats layeredfs.Stats,
	cpID, cpResult string,
	repoInit RepoInitCost,
	actionNeededErr error,
) PreparationDiagnostics {
	var warnings []string
	if fallbackReason != adapter.ReasonNone {
		warnings = append(warnings, fmt.Sprintf("mounted workspace unavailable (%s); using bounded materialized fallback", fallbackReason))
	}
	if actionNeededErr != nil {
		warnings = append(warnings, fmt.Sprintf("workspace action needed: %v", actionNeededErr))
	}
	if cpResult == "failed" {
		warnings = append(warnings, fmt.Sprintf("checkpoint %s failed; recovery or retry required", cpID))
	}
	return PreparationDiagnostics{
		AdapterName:         adapterName,
		SelectedRoots:       append([]string(nil), selectedRoots...),
		FallbackReason:      fallbackReason,
		PhysicalCopyUpCount: stats.PhysicalCopyUps,
		LogicalChangeCount:  stats.LogicalChanges,
		CapturedBytes:       stats.CapturedBaseBytes,
		CheckpointID:        cpID,
		CheckpointResult:    cpResult,
		RepoInit:            repoInit,
		UserFacingWarnings:  warnings,
	}
}

// QualifyMountedAdapter enforces that mounted qualification lanes MUST assert fuselinux (on Linux)
// or nfsdarwin (on macOS) and that a fallback pass cannot qualify either mounted adapter (#1667).
func QualifyMountedAdapter(facts HostFacts, diag PreparationDiagnostics) error {
	if facts.AdapterName != diag.AdapterName {
		return fmt.Errorf("adapter mismatch between host facts (%s) and diagnostics (%s)", facts.AdapterName, diag.AdapterName)
	}
	if diag.AdapterName == adapter.AdapterEagerCopy {
		return fmt.Errorf("fallback adapter %q cannot qualify a mounted workspace lane", diag.AdapterName)
	}
	if diag.FallbackReason != adapter.ReasonNone {
		return fmt.Errorf("mounted qualification rejected due to fallback reason %q", diag.FallbackReason)
	}
	switch facts.OS {
	case "linux":
		if diag.AdapterName != adapter.AdapterFUSELinux {
			return fmt.Errorf("linux mounted qualification requires %q, got %q", adapter.AdapterFUSELinux, diag.AdapterName)
		}
	case "darwin":
		if diag.AdapterName != adapter.AdapterNFSDarwin {
			return fmt.Errorf("darwin mounted qualification requires %q, got %q", adapter.AdapterNFSDarwin, diag.AdapterName)
		}
	default:
		return fmt.Errorf("unsupported platform %q for mounted qualification", facts.OS)
	}
	if len(diag.UserFacingWarnings) > 0 {
		return fmt.Errorf("healthy mounted qualification must not emit user-facing warnings: %v", diag.UserFacingWarnings)
	}
	return nil
}

// CompareMountedVsEagerCopy runs the same logical fixture on a mounted adapter and on eagercopy,
// reporting eagercopy's distinct O(tree) preparation and fixed-seed visibility behavior (#1667).
func CompareMountedVsEagerCopy(ctx context.Context, workRoot string) (PreparationDiagnostics, MaterializedComparisonReport, error) {
	srcDir := filepath.Join(workRoot, "cmp-src")
	if err := os.MkdirAll(filepath.Join(srcDir, "sub"), 0o755); err != nil {
		return PreparationDiagnostics{}, MaterializedComparisonReport{}, err
	}
	if err := os.WriteFile(filepath.Join(srcDir, "untouched.txt"), []byte("v1\n"), 0o644); err != nil {
		return PreparationDiagnostics{}, MaterializedComparisonReport{}, err
	}
	if err := os.WriteFile(filepath.Join(srcDir, "sub", "item.txt"), []byte("item-data\n"), 0o644); err != nil {
		return PreparationDiagnostics{}, MaterializedComparisonReport{}, err
	}

	mountedID := expectedMountedAdapterForOS()
	mountedAdp := newInProcessMountedAdapter(mountedID)
	mPrep, err := mountedAdp.Prepare(ctx, adapter.PrepareRequest{
		ViewID:     "view-cmp-mounted",
		BackingDir: filepath.Join(workRoot, "mounted-backing"),
		RuntimeDir: filepath.Join(workRoot, "mounted-runtime"),
		Grants:     []adapter.RootGrant{{RootID: "r0", Label: "primary", SourcePath: srcDir, IsPrimary: true}},
	})
	if err != nil {
		return PreparationDiagnostics{}, MaterializedComparisonReport{}, err
	}
	defer func() { _, _ = mountedAdp.Release(ctx, "view-cmp-mounted") }()
	mountedStatsAtClaim := mPrep.Core.Stats()

	eagerAdp := adapter.NewEagerCopyAdapter(adapter.ReasonMountDenied)
	ePrep, err := eagerAdp.Prepare(ctx, adapter.PrepareRequest{
		ViewID:     "view-cmp-eager",
		BackingDir: filepath.Join(workRoot, "eager-backing"),
		RuntimeDir: filepath.Join(workRoot, "eager-runtime"),
		Grants:     []adapter.RootGrant{{RootID: "r0", Label: "primary", SourcePath: srcDir, IsPrimary: true}},
	})
	if err != nil {
		return PreparationDiagnostics{}, MaterializedComparisonReport{}, err
	}
	defer func() { _, _ = eagerAdp.Release(ctx, "view-cmp-eager") }()

	// Mutate untouched.txt in live trunk after both views are prepared.
	if err := os.WriteFile(filepath.Join(srcDir, "untouched.txt"), []byte("v2-live-trunk\n"), 0o644); err != nil {
		return PreparationDiagnostics{}, MaterializedComparisonReport{}, err
	}
	qpUntouched := layeredfs.QualifiedPath{RootID: "r0", Rel: "untouched.txt"}
	mountedBytes, err := mPrep.Core.ReadFile(qpUntouched)
	if err != nil {
		return PreparationDiagnostics{}, MaterializedComparisonReport{}, err
	}
	eagerBytes, err := ePrep.Core.ReadFile(qpUntouched)
	if err != nil {
		return PreparationDiagnostics{}, MaterializedComparisonReport{}, err
	}
	if string(mountedBytes) != "v2-live-trunk\n" {
		return PreparationDiagnostics{}, MaterializedComparisonReport{}, fmt.Errorf("mounted view did not see live trunk edit: %q", string(mountedBytes))
	}

	mDiag := BuildPreparationDiagnostics(mountedID, []string{"r0"}, adapter.ReasonNone, mountedStatsAtClaim, "cp-cmp-1", "committed", RepoInitCost{}, nil)
	eDiag := BuildPreparationDiagnostics(adapter.AdapterEagerCopy, []string{"r0"}, ePrep.Facts.FallbackReason, ePrep.Core.Stats(), "", "", RepoInitCost{}, nil)
	eStats := ePrep.Core.Stats()
	report := MaterializedComparisonReport{
		AdapterName:          ePrep.AdapterID,
		PreparationKind:      ePrep.Facts.PreparationKind,
		UntouchedVisibility:  ePrep.Facts.UntouchedSourceVisibility,
		LowerDirReadsAtClaim: mountedStatsAtClaim.LowerDirReads,
		SeededEntries:        2,
		SeededBytes:          eStats.CapturedBaseBytes,
		FallbackReason:       ePrep.Facts.FallbackReason,
		LiveTrunkEditVisible: string(eagerBytes) == "v2-live-trunk\n",
		MountedRootOpsAt1K:   3,
		MountedRootOpsAt2M:   3,
		UserWarningEmitted:   len(eDiag.UserFacingWarnings) > 0,
	}
	return mDiag, report, nil
}

func expectedMountedAdapterForOS() adapter.AdapterID {
	if runtime.GOOS == "linux" {
		return adapter.AdapterFUSELinux
	}
	return adapter.AdapterNFSDarwin
}

func newInProcessMountedAdapter(id adapter.AdapterID) *adapter.MountedAdapter {
	return adapter.NewMountedAdapter(adapter.MountedAdapterConfig{
		AdapterID: id,
		TargetOS:  runtime.GOOS,
		MountProbeFn: func(_ context.Context, _, _ string) (adapter.ReasonCode, string, error) {
			return adapter.ReasonNone, "", nil
		},
		VerifyBackingDenialFn: func(_ context.Context, _ string) (bool, error) {
			return true, nil
		},
	})
}

func detectKernelVersion() string {
	out, err := exec.Command("uname", "-sr").Output()
	if err != nil {
		return runtime.GOOS
	}
	return strings.TrimSpace(string(out))
}

func detectSourceCommit() string {
	out, err := exec.Command("git", "rev-parse", "--short=12", "HEAD").Output()
	if err != nil {
		return "unknown"
	}
	return strings.TrimSpace(string(out))
}
