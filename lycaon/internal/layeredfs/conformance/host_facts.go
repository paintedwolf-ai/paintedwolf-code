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
// BackingFilesystem is probed from the real backing directory via statfs(2) — never guessed from
// the platform (the Linux test host /mnt/data1 is ZFS, not ext4/xfs). MountCacheSettings comes
// from the caller or the adapter's declared CacheRevalidation — never a fabricated zero-timeout
// string. When a fact cannot be probed it is reported as "unknown" (Lead A B01 review defect 4).
func CollectHostFacts(
	adapterName adapter.AdapterID,
	mountCacheSettings, workloadFixture, backingDir string,
	cacheReval *adapter.CacheRevalidation,
) HostFacts {
	installShape := "macos_builtin_nfs_client_no_extension"
	if runtime.GOOS == "linux" {
		if _, err := exec.LookPath("fusermount3"); err == nil {
			installShape = "linux_package_fuse3_unprivileged_user"
		} else {
			installShape = "linux_fuse3_unavailable"
		}
	}
	fsKind := "unknown"
	if name, ok := statfsFilesystemType(backingDir); ok && name != "" {
		fsKind = name
	}
	if mountCacheSettings == "" {
		if cacheReval != nil {
			mountCacheSettings = fmt.Sprintf("mode=%s,explicit_invalidation=%t,attribute_ttl=%s",
				cacheReval.Mode, cacheReval.ExplicitInvalidation, cacheReval.AttributeTTL)
		} else {
			mountCacheSettings = "unknown"
		}
	}
	if workloadFixture == "" {
		workloadFixture = "three-root-mixed-repo-and-assets-v1"
	}
	return HostFacts{
		SourceCommit:            detectSourceCommit(),
		OS:                      runtime.GOOS,
		Arch:                    runtime.GOARCH,
		KernelVersion:           detectKernelVersion(),
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

// MountedSideMeasurement carries values MEASURED against a real mounted adapter
// (fuselinux on Linux, nfsdarwin on macOS) by the external qualification harness.
// In-process mock adapters and hardcoded constants are not conformance evidence
// (#1667; Lead A B01 review defect 3): CompareMountedVsEagerCopy refuses to run
// unless Available is true, and every mounted-side field in the resulting report
// comes from these measured inputs.
type MountedSideMeasurement struct {
	Available   bool
	Stats       layeredfs.Stats // Core.Stats() sampled at claim time on the real mount
	RootOpsAt1K int64           // measured root-level ops with the 1K-entry fixture
	RootOpsAt2M int64           // measured root-level ops with the 2M-entry fixture
}

// CompareMountedVsEagerCopy measures eagercopy's O(tree) preparation and
// fixed-seed visibility behavior against a real eager adapter, and combines it
// with mounted-side values measured externally on a real mount. The mounted
// side is never simulated here: callers must supply a MountedSideMeasurement
// from a real fuselinux/nfsdarwin harness, or the function returns an error
// rather than fabricating report fields (#1667; Lead A B01 review defect 3).
func CompareMountedVsEagerCopy(ctx context.Context, workRoot string, mounted MountedSideMeasurement) (PreparationDiagnostics, MaterializedComparisonReport, error) {
	if !mounted.Available {
		return PreparationDiagnostics{}, MaterializedComparisonReport{},
			fmt.Errorf("mounted side must be measured against a real mounted adapter (fuselinux/nfsdarwin); in-process mocks and hardcoded constants are not conformance evidence (#1667)")
	}
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

	// Mutate untouched.txt in live trunk after the view is prepared; the eager
	// view must keep showing the seeded content. Mounted-side live-edit
	// visibility is asserted by the real-mount harness, not here.
	if err := os.WriteFile(filepath.Join(srcDir, "untouched.txt"), []byte("v2-live-trunk\n"), 0o644); err != nil {
		return PreparationDiagnostics{}, MaterializedComparisonReport{}, err
	}
	qpUntouched := layeredfs.QualifiedPath{RootID: "r0", Rel: "untouched.txt"}
	eagerBytes, err := ePrep.Core.ReadFile(qpUntouched)
	if err != nil {
		return PreparationDiagnostics{}, MaterializedComparisonReport{}, err
	}

	mDiag := BuildPreparationDiagnostics(expectedMountedAdapterForOS(), []string{"r0"}, adapter.ReasonNone, mounted.Stats, "cp-cmp-1", "committed", RepoInitCost{}, nil)
	eDiag := BuildPreparationDiagnostics(adapter.AdapterEagerCopy, []string{"r0"}, ePrep.Facts.FallbackReason, ePrep.Core.Stats(), "", "", RepoInitCost{}, nil)
	report := MaterializedComparisonReport{
		AdapterName:          ePrep.AdapterID,
		PreparationKind:      ePrep.Facts.PreparationKind,
		UntouchedVisibility:  ePrep.Facts.UntouchedSourceVisibility,
		LowerDirReadsAtClaim: mounted.Stats.LowerDirReads,
		SeededEntries:        ePrep.ObservedEntries,
		SeededBytes:          ePrep.ObservedBytes,
		FallbackReason:       ePrep.Facts.FallbackReason,
		LiveTrunkEditVisible: string(eagerBytes) == "v2-live-trunk\n",
		MountedRootOpsAt1K:   mounted.RootOpsAt1K,
		MountedRootOpsAt2M:   mounted.RootOpsAt2M,
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
