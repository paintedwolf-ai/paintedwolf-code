package conformance

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/layeredfs"
	"github.com/lycaon/lycaon/internal/layeredfs/adapter"
)

// TestCollectHostFactsProbesBackingFilesystem verifies BackingFilesystem comes
// from a real statfs probe of the backing directory, and "unknown" when no
// probe path is available — never a platform guess (#1667; defect 4).
func TestCollectHostFactsProbesBackingFilesystem(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()

	facts := CollectHostFacts(adapter.AdapterFUSELinux, "", "", dir, nil)
	if facts.BackingFilesystem == "" {
		t.Fatalf("BackingFilesystem must not be empty")
	}
	if name, ok := statfsFilesystemType(dir); ok {
		if facts.BackingFilesystem != name {
			t.Fatalf("BackingFilesystem = %q, want probed statfs type %q", facts.BackingFilesystem, name)
		}
	} else if facts.BackingFilesystem != "unknown" {
		t.Fatalf("BackingFilesystem = %q, want \"unknown\" when probe unavailable", facts.BackingFilesystem)
	}

	noDir := CollectHostFacts(adapter.AdapterFUSELinux, "", "", "", nil)
	if noDir.BackingFilesystem != "unknown" {
		t.Fatalf("BackingFilesystem without probe path = %q, want \"unknown\"", noDir.BackingFilesystem)
	}
}

// TestCollectHostFactsMountCacheSettings verifies MountCacheSettings comes from
// the caller or the adapter's declared CacheRevalidation — never a fabricated
// zero-timeout string (#1667; defect 4).
func TestCollectHostFactsMountCacheSettings(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()

	explicit := CollectHostFacts(adapter.AdapterFUSELinux, "fuse3,attr_timeout=5", "", dir, nil)
	if explicit.MountCacheSettings != "fuse3,attr_timeout=5" {
		t.Fatalf("explicit MountCacheSettings overridden: %q", explicit.MountCacheSettings)
	}

	unknown := CollectHostFacts(adapter.AdapterFUSELinux, "", "", dir, nil)
	if unknown.MountCacheSettings != "unknown" {
		t.Fatalf("MountCacheSettings without source = %q, want \"unknown\"", unknown.MountCacheSettings)
	}

	derived := CollectHostFacts(adapter.AdapterFUSELinux, "", "", dir, &adapter.CacheRevalidation{
		Mode:                 "readdir_plus",
		ExplicitInvalidation: true,
		AttributeTTL:         1500 * time.Millisecond,
	})
	for _, want := range []string{"mode=readdir_plus", "explicit_invalidation=true", "attribute_ttl=1.5s"} {
		if !strings.Contains(derived.MountCacheSettings, want) {
			t.Fatalf("derived MountCacheSettings %q missing %q", derived.MountCacheSettings, want)
		}
	}
}

// TestCompareMountedVsEagerCopyRequiresMeasuredMountedSide verifies the
// comparison refuses to fabricate mounted-side report fields when no real
// mount measurement is supplied (#1667; defect 3).
func TestCompareMountedVsEagerCopyRequiresMeasuredMountedSide(t *testing.T) {
	t.Parallel()
	_, _, err := CompareMountedVsEagerCopy(context.Background(), t.TempDir(), MountedSideMeasurement{})
	if err == nil {
		t.Fatalf("expected error for unmeasured mounted side")
	}
	if !strings.Contains(err.Error(), "real mounted adapter") {
		t.Fatalf("error should name the real-mount requirement, got: %v", err)
	}
}

// TestCompareMountedVsEagerCopyMeasuresEagerSide verifies the eager side is
// measured from a real adapter (seeded entries/bytes from ObservedEntries/
// ObservedBytes) and mounted-side report fields come only from the supplied
// measurement (#1667; defect 3).
func TestCompareMountedVsEagerCopyMeasuresEagerSide(t *testing.T) {
	t.Parallel()
	mounted := MountedSideMeasurement{
		Available:   true,
		Stats:       layeredfs.Stats{LowerDirReads: 7},
		RootOpsAt1K: 11,
		RootOpsAt2M: 13,
	}
	_, report, err := CompareMountedVsEagerCopy(context.Background(), t.TempDir(), mounted)
	if err != nil {
		t.Fatalf("CompareMountedVsEagerCopy: %v", err)
	}
	if report.LowerDirReadsAtClaim != 7 {
		t.Fatalf("LowerDirReadsAtClaim = %d, want measured 7", report.LowerDirReadsAtClaim)
	}
	if report.MountedRootOpsAt1K != 11 || report.MountedRootOpsAt2M != 13 {
		t.Fatalf("MountedRootOpsAt1K/2M = %d/%d, want measured 11/13", report.MountedRootOpsAt1K, report.MountedRootOpsAt2M)
	}
	if report.SeededEntries <= 0 {
		t.Fatalf("SeededEntries = %d, want real adapter ObservedEntries > 0", report.SeededEntries)
	}
	if report.SeededBytes <= 0 {
		t.Fatalf("SeededBytes = %d, want real adapter ObservedBytes > 0", report.SeededBytes)
	}
	if report.LiveTrunkEditVisible {
		t.Fatalf("eager view must not see live trunk edits made after prepare")
	}
	if report.AdapterName != adapter.AdapterEagerCopy {
		t.Fatalf("report adapter = %q, want eagercopy", report.AdapterName)
	}
}
