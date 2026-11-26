package definition

import (
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestRunSnapshotRestoresEveryBundledDefinition(t *testing.T) {
	reg, err := RegistryFromDirs("")
	testutil.FailErr(t, "load catalog", err)
	for key, m := range reg.All() {
		t.Run(key, func(t *testing.T) {
			data, err := EncodeRunManifest(m)
			testutil.FailErr(t, "capture admitted workflow", err)
			got, err := DecodeRunManifest(data)
			testutil.FailErr(t, "restore run definition", err)
			if diff := cmp.Diff(m, got, cmpopts.EquateEmpty()); diff != "" {
				t.Fatalf("snapshot changed resolved workflow:\n%s", diff)
			}
		})
	}
}

func TestRunSnapshotRejectsUnknownShapes(t *testing.T) {
	for _, raw := range []string{"manifest: [", "snapshot_version: 2", "snapshot_version: 1", "snapshot_version: 1\nfuture: true"} {
		if _, err := DecodeRunManifest(raw); err == nil {
			t.Errorf("accepted %q", raw)
		}
	}
}

func TestDecodeRunManifestCacheReturnsDetachedClone(t *testing.T) {
	reg, err := RegistryFromDirs("")
	testutil.FailErr(t, "load catalog", err)
	for _, m := range reg.All() {
		if len(m.Phases) == 0 {
			continue
		}
		raw, err := EncodeRunManifest(m)
		testutil.FailErr(t, "encode run manifest", err)
		first, err := DecodeRunManifest(raw)
		testutil.FailErr(t, "first decode", err)
		originalPhase := first.Phases[0]
		first.Phases[0] = "mutated-phase"

		second, err := DecodeRunManifest(raw)
		testutil.FailErr(t, "second decode", err)
		if second.Phases[0] != originalPhase {
			t.Fatalf("cache leaked caller mutation: got %q, want %q", second.Phases[0], originalPhase)
		}
		return
	}
	t.Fatal("no bundled workflow with phases found")
}
