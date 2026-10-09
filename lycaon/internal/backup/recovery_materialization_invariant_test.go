package backup

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
)

func TestRecoveryMaterializationRefusesIncompleteSourcesWithoutPublishing(t *testing.T) {
	for _, failure := range []string{"canceled", "blocked parent", "missing source"} {
		t.Run(failure, func(t *testing.T) {
			root := t.TempDir()
			source := filepath.Join(root, "source")
			testutil.FailErr(t, "seed original payload", os.WriteFile(source, []byte("original bytes"), 0o600))
			parent := filepath.Join(root, "parent")
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			switch failure {
			case "canceled":
				cancel()
			case "blocked parent":
				testutil.FailErr(t, "occupy destination parent", os.WriteFile(parent, []byte("retained bytes"), 0o600))
			case "missing source":
				source = filepath.Join(root, "missing")
			}
			shared, err := copySnapshotRegular(ctx, source, filepath.Join(parent, "payload"), 0o600)
			if err == nil || shared {
				t.Fatalf("incomplete source reported successful materialization: shared=%v, err=%v", shared, err)
			}
			got, err := os.ReadFile(filepath.Join(root, "source"))
			testutil.FailErr(t, "read unchanged source", err)
			if string(got) != "original bytes" {
				t.Fatal("failed snapshot changed original payload")
			}
		})
	}
}

func TestRecoverySymlinkCapturesOnlyItsTargetText(t *testing.T) {
	root := t.TempDir()
	link := filepath.Join(root, "link")
	target := filepath.Join(root, "unrelated-user-data")
	testutil.FailErr(t, "seed unselected data", os.WriteFile(target, []byte("unselected bytes"), 0o600))
	testutil.FailErr(t, "seed retained link", os.Symlink(target, link))
	destination := filepath.Join(root, "snapshot")
	shared, err := copySnapshotSource(t.Context(), archiveSource{path: link, kind: fileKindSymlink}, destination)
	testutil.FailErr(t, "capture link representation", err)
	got, err := os.ReadFile(destination)
	testutil.FailErr(t, "read captured representation", err)
	if shared || string(got) != target {
		t.Fatalf("symlink capture followed unselected data: shared=%v, content=%q", shared, got)
	}
	if _, err := copySnapshotSource(t.Context(), archiveSource{path: filepath.Join(root, "absent-link"), kind: fileKindSymlink}, filepath.Join(root, "absent-capture")); !os.IsNotExist(err) {
		t.Fatalf("missing link was materialized: %v", err)
	}
}

func TestRecoveryUsageSeparatesCopiedAndSharedPayloadFromDatabase(t *testing.T) {
	root := t.TempDir()
	copied, shared := filepath.Join(root, "copied"), filepath.Join(root, "shared")
	testutil.FailErr(t, "seed copied payload", os.WriteFile(copied, []byte("copy"), 0o600))
	testutil.FailErr(t, "seed shared payload", os.WriteFile(shared, []byte("shared extent"), 0o600))
	usage := RecoveryCaptureUsage{DatabaseBytes: 4096}
	testutil.FailErr(t, "account copied materialization", usage.addFile(copied, false))
	testutil.FailErr(t, "account shared materialization", usage.addFile(shared, true))
	if usage != (RecoveryCaptureUsage{DatabaseBytes: 4096, PayloadCopiedBytes: 4, PayloadSharedBytes: 13, CopiedFiles: 1, SharedFiles: 1}) {
		t.Fatalf("logical shared extents were charged as copied storage: %+v", usage)
	}
}

func TestRecoveryCaptureRefusesAnOccupiedDestination(t *testing.T) {
	path := filepath.Join(t.TempDir(), "retained-recovery")
	testutil.FailErr(t, "seed existing recovery", os.WriteFile(path, []byte("retained bytes"), 0o600))
	if _, _, err := captureRecoveryDirectory(t.Context(), CreateOpts{}, path); !os.IsExist(err) {
		t.Fatalf("existing recovery was admitted as a new capture: %v", err)
	}
	got, err := os.ReadFile(path)
	testutil.FailErr(t, "read existing recovery", err)
	if string(got) != "retained bytes" {
		t.Fatal("refused capture changed existing recovery")
	}
}
