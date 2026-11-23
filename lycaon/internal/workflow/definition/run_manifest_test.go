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
