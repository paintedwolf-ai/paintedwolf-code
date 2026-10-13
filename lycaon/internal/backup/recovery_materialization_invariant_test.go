package backup

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/enginepaths"
	"github.com/lycaon/lycaon/internal/sourceblob"
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
			shared, err := copySnapshotRegular(ctx, archiveSource{path: source, mode: 0o600}, filepath.Join(parent, "payload"))
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

func TestRecoverySharingSelectsOnlyImmutableCanonicalSourceBodies(t *testing.T) {
	root := t.TempDir()
	canonical := filepath.ToSlash(filepath.Join(enginepaths.SourceContentDirName, "aa", strings.Repeat("b", 62)+".zst"))
	for _, item := range []struct {
		rel      string
		mode     os.FileMode
		eligible bool
	}{
		{canonical, 0o400, true}, {canonical, 0o600, false},
		{strings.Replace(canonical, "source-content", "drafts", 1), 0o400, false},
		{strings.Replace(canonical, "aa/", "AA/", 1), 0o400, false},
		{strings.Replace(canonical, ".zst", ".tmp", 1), 0o400, false},
		{filepath.Join(enginepaths.SourceContentDirName, "named.zst"), 0o400, false},
	} {
		path := filepath.Join(root, filepath.FromSlash(item.rel))
		testutil.FailErr(t, "create body parent", os.MkdirAll(filepath.Dir(path), 0o700))
		testutil.FailErr(t, "replace fixture body", os.RemoveAll(path))
		testutil.FailErr(t, "write body", os.WriteFile(path, []byte("body"), item.mode))
		info, err := os.Lstat(path)
		testutil.FailErr(t, "inspect body", err)
		want := item.eligible && runtime.GOOS == "linux"
		if got := immutableRecoveryBody(item.rel, info); got != want {
			t.Fatalf("sharing eligibility for %s mode %o = %v, want %v", item.rel, item.mode, got, want)
		}
	}
}

func TestRecoveryCapturePreservesSourceBodiesAfterLiveCollection(t *testing.T) {
	root := t.TempDir()
	input := filepath.Join(t.TempDir(), "source.txt")
	testutil.FailErr(t, "write source", os.WriteFile(input, []byte("retained source bytes"), 0o600))
	store := sourceblob.New(filepath.Join(root, enginepaths.SourceContentDirName))
	defer func() { _ = store.Close() }()
	captured, err := store.PutFile(t.Context(), input)
	testutil.FailErr(t, "publish immutable source", err)
	rel := filepath.ToSlash(filepath.Join(enginepaths.SourceContentDirName, captured.Rel))
	files := map[string]archiveSource{}
	testutil.FailErr(t, "collect immutable store", collectDurableDir(t.Context(), root, enginepaths.SourceContentDirName, files, branchCapture{all: true}, 100))
	source := files[rel]
	before, err := os.Stat(source.path)
	testutil.FailErr(t, "inspect live body", err)
	dest := filepath.Join(t.TempDir(), filepath.FromSlash(rel))
	shared, err := copySnapshotSource(t.Context(), source, dest)
	testutil.FailErr(t, "capture retained source", err)
	after, err := os.Stat(dest)
	testutil.FailErr(t, "inspect retained body", err)
	if after.Mode().Perm() != 0o400 || before.Mode().Perm() != 0o400 {
		t.Fatal("capture changed immutable body permissions")
	}
	if runtime.GOOS == "linux" && !shared {
		t.Fatal("Linux capture did not share immutable body storage")
	}
	testutil.FailErr(t, "collect live body", store.Remove(captured.Rel))
	sum, size, err := sourceDigest(t.Context(), archiveSource{path: dest, kind: fileKindRegular})
	testutil.FailErr(t, "verify retained body after collection", err)
	testutil.FailErr(t, "validate captured body integrity", verifySnapshotFile(t.Context(), filepath.Dir(filepath.Dir(filepath.Dir(dest))), FileEntry{RelPath: rel, SHA256: sum, Size: size}))
	restored := filepath.Join(t.TempDir(), "restored")
	_, err = copySnapshotRegular(t.Context(), archiveSource{path: dest, mode: 0o400}, restored)
	testutil.FailErr(t, "restore without live body", err)
	restoredSum, restoredSize, err := sourceDigest(t.Context(), archiveSource{path: restored, kind: fileKindRegular})
	testutil.FailErr(t, "verify restored bytes", err)
	if restoredSum != sum || restoredSize != size {
		t.Fatal("restore changed retained body")
	}
}

func TestRecoverySpaceEstimateExcludesOnlySameVolumeSharedBodies(t *testing.T) {
	root := t.TempDir()
	immutable := filepath.Join(root, enginepaths.SourceContentDirName, "aa", strings.Repeat("b", 62)+".zst")
	mutable := filepath.Join(root, "drafts", "body")
	for path, mode := range map[string]os.FileMode{immutable: 0o400, mutable: 0o600} {
		testutil.FailErr(t, "create payload directory", os.MkdirAll(filepath.Dir(path), 0o700))
		testutil.FailErr(t, "seed payload", os.WriteFile(path, []byte("four"), mode))
	}
	full, metadata, err := recoveryPayloadEstimate(t.Context(), root, 100, false)
	testutil.FailErr(t, "estimate independent copies", err)
	shared, sharedMetadata, err := recoveryPayloadEstimate(t.Context(), root, 100, true)
	testutil.FailErr(t, "estimate shared recovery", err)
	want := uint64(108)
	if runtime.GOOS == "linux" {
		want = 104
	}
	if full != 108+metadata || shared != want+metadata || metadata != sharedMetadata || metadata == 0 {
		t.Fatalf("recovery estimates: full=%d shared=%d metadata=%d sharedMetadata=%d", full, shared, metadata, sharedMetadata)
	}
}

func TestRecoveryBodyLinksPreserveModesAndFallBackAcrossVolumes(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "body")
	testutil.FailErr(t, "seed immutable body", os.WriteFile(path, []byte("retained"), 0o400))
	source := archiveSource{path: path, mode: 0o400, shareBody: true}
	dest := filepath.Join(root, "snapshot")
	linked, err := linkSnapshotBody(source, dest)
	testutil.FailErr(t, "attempt immutable link", err)
	if runtime.GOOS != "linux" {
		if linked {
			t.Fatal("non-Linux snapshot used hard links")
		}
		return
	}
	if !linked {
		t.Fatal("same-volume immutable body was not linked")
	}
	live, err := os.Stat(path)
	testutil.FailErr(t, "inspect live inode", err)
	retained, err := os.Stat(dest)
	testutil.FailErr(t, "inspect retained inode", err)
	if !os.SameFile(live, retained) || retained.Mode().Perm() != 0o400 {
		t.Fatal("immutable link did not preserve the inode and read-only mode")
	}
	if _, err := linkSnapshotBody(source, dest); !os.IsExist(err) {
		t.Fatalf("occupied linked destination was overwritten: %v", err)
	}
	cross, err := os.MkdirTemp("/dev/shm", "paintedwolf-recovery-") //nolint:usetesting // The destination must be on a separate filesystem.
	testutil.FailErr(t, "create separate-volume capture directory", err)
	t.Cleanup(func() { _ = os.RemoveAll(cross) })
	if sharedRecoveryBodyOnVolume(cross, source) {
		t.Fatal("cross-volume fixture is not on a separate filesystem")
	}
	target := filepath.Join(cross, "body")
	linked, err = linkSnapshotBody(source, target)
	testutil.FailErr(t, "cross-volume link fallback", err)
	if linked {
		t.Fatal("cross-volume body reported shared")
	}
	shared, err := copySnapshotRegular(t.Context(), source, target)
	testutil.FailErr(t, "capture across volumes", err)
	copied, err := os.Stat(target)
	testutil.FailErr(t, "inspect independent body", err)
	if shared || os.SameFile(live, copied) || copied.Mode().Perm() != 0o400 {
		t.Fatal("cross-volume fallback did not make an independent read-only copy")
	}
}
