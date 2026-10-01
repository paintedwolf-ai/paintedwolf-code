package fseffect_test

import (
	"bytes"
	"errors"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"sync/atomic"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/fseffect"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestRemovePreservesMissingPathIdentity(t *testing.T) {
	for _, rel := range []string{"missing.txt", "missing/missing.txt", "missing/nested/missing.txt"} {
		t.Run(rel, func(t *testing.T) {
			root := t.TempDir()
			err := fseffect.Remove(fseffect.RemoveRequest{Location: fseffect.PathLocation(filepath.Join(root, rel))})
			if !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("remove error = %v, want missing-path identity", err)
			}
			entries, err := os.ReadDir(root)
			testutil.FailErr(t, "read unchanged root", err)
			if len(entries) != 0 {
				t.Fatalf("missing-path removal created %d entries", len(entries))
			}
		})
	}
}

func TestReplaceRefusesSymlinkedParent(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	testutil.FailErr(t, "symlink parent", os.Symlink(outside, filepath.Join(root, "link")))
	_, err := fseffect.Replace(fseffect.ReplaceRequest{
		Location: fseffect.Location{Root: root, Rel: "link/escaped.txt"},
		Source:   bytes.NewReader([]byte("escaped")), BeforeCommit: func(fseffect.Target, fseffect.Result) error { return nil },
	})
	if !errors.Is(err, fseffect.ErrSymlink) {
		t.Fatalf("Replace error = %v, want ErrSymlink", err)
	}
	if _, statErr := os.Stat(filepath.Join(outside, "escaped.txt")); !os.IsNotExist(statErr) {
		t.Fatalf("outside target was created: %v", statErr)
	}
}

func TestConditionalRemoveDeletesVerifiedFile(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "document.txt")
	testutil.FailErr(t, "seed target", os.WriteFile(target, []byte("expected"), 0o600))
	err := fseffect.Remove(fseffect.RemoveRequest{
		Location: fseffect.Location{Root: root, Rel: "document.txt"},
		BeforeCommit: func(target fseffect.Target) error {
			f, err := target.Open()
			if err != nil {
				return err
			}
			defer func() { _ = f.Close() }()
			got := make([]byte, len("expected"))
			_, err = f.Read(got)
			if err != nil || string(got) != "expected" {
				return errors.New("wrong removal target")
			}
			return nil
		},
	})
	testutil.FailErr(t, "conditional remove", err)
	if _, err := os.Stat(target); !os.IsNotExist(err) {
		t.Fatalf("removed target still exists: %v", err)
	}
}

func TestConditionalRemoveNeverDeletesConcurrentReplacement(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "document.txt")
	testutil.FailErr(t, "seed target", os.WriteFile(target, []byte("original"), 0o600))
	sentinel := errors.New("changed")
	err := fseffect.Remove(fseffect.RemoveRequest{
		Location: fseffect.Location{Root: root, Rel: "document.txt"},
		BeforeCommit: func(fseffect.Target) error {
			testutil.FailErr(t, "write replacement", os.WriteFile(target, []byte("replacement"), 0o600))
			return sentinel
		},
	})
	if !errors.Is(err, sentinel) {
		t.Fatalf("Remove error = %v, want changed", err)
	}
	got, readErr := os.ReadFile(target)
	testutil.FailErr(t, "read replacement", readErr)
	if string(got) != "replacement" {
		t.Fatalf("concurrent replacement = %q", got)
	}
}

func TestRemoveRefusesSymlinkedParent(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	testutil.FailErr(t, "seed outside", os.WriteFile(filepath.Join(outside, "keep.txt"), []byte("keep"), 0o600))
	testutil.FailErr(t, "symlink parent", os.Symlink(outside, filepath.Join(root, "link")))
	err := fseffect.Remove(fseffect.RemoveRequest{Location: fseffect.Location{Root: root, Rel: "link/keep.txt"}})
	if !errors.Is(err, fseffect.ErrSymlink) {
		t.Fatalf("Remove error = %v, want ErrSymlink", err)
	}
	if _, err := os.Stat(filepath.Join(outside, "keep.txt")); err != nil {
		t.Fatalf("outside target changed: %v", err)
	}
}

func TestReplaceCommitsAndVerifiesThroughHeldParent(t *testing.T) {
	root := t.TempDir()
	result, err := fseffect.Replace(fseffect.ReplaceRequest{
		Location: fseffect.Location{Root: root, Rel: "nested/file.txt"},
		Source:   bytes.NewReader([]byte("complete\n")), Mode: 0o640,
		BeforeCommit: func(target fseffect.Target, _ fseffect.Result) error {
			if _, err := target.Lstat(); !os.IsNotExist(err) {
				return errors.New("destination appeared")
			}
			return nil
		},
	})
	testutil.FailErr(t, "Replace", err)
	if result.Bytes != int64(len("complete\n")) {
		t.Fatalf("Replace bytes = %d", result.Bytes)
	}
	got, err := os.ReadFile(filepath.Join(root, "nested", "file.txt"))
	testutil.FailErr(t, "ReadFile", err)
	if string(got) != "complete\n" {
		t.Fatalf("committed bytes = %q", got)
	}
}

func TestOpenWriteRefusesFinalSymlink(t *testing.T) {
	root := t.TempDir()
	outside := filepath.Join(t.TempDir(), "outside")
	testutil.FailErr(t, "seed outside", os.WriteFile(outside, []byte("safe"), 0o600))
	testutil.FailErr(t, "symlink target", os.Symlink(outside, filepath.Join(root, "out")))
	_, err := fseffect.OpenWrite(fseffect.Location{Root: root, Rel: "out"}, false, 0o600)
	if !errors.Is(err, fseffect.ErrSymlink) {
		t.Fatalf("OpenWrite error = %v, want ErrSymlink", err)
	}
	got, readErr := os.ReadFile(outside)
	testutil.FailErr(t, "read outside", readErr)
	if string(got) != "safe" {
		t.Fatalf("outside bytes = %q", got)
	}
}

func TestUpdateModeAppliesThroughHeldParent(t *testing.T) {
	root := t.TempDir()
	testutil.FailErr(t, "mkdir nested", os.MkdirAll(filepath.Join(root, "nested"), 0o755))
	target := filepath.Join(root, "nested", "run.sh")
	testutil.FailErr(t, "seed target", os.WriteFile(target, []byte("#!/bin/sh\n"), 0o644))
	_, err := fseffect.UpdateMode(fseffect.ModeUpdateRequest{
		Location: fseffect.Location{Root: root, Rel: "nested/run.sh"},
		Update:   func(os.FileMode) os.FileMode { return 0o755 },
	})
	testutil.FailErr(t, "update mode", err)
	info, err := os.Stat(target)
	testutil.FailErr(t, "stat target", err)
	if info.Mode().Perm() != 0o755 {
		t.Fatalf("mode = %o, want 755", info.Mode().Perm())
	}
}

func TestUpdateModeGuardsTheHeldFile(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "locked.txt")
	testutil.FailErr(t, "seed target", os.WriteFile(target, []byte("expected"), 0o444))
	guardErr := errors.New("stale target")
	_, err := fseffect.UpdateMode(fseffect.ModeUpdateRequest{
		Location: fseffect.Location{Root: root, Rel: "locked.txt"},
		Update:   func(mode os.FileMode) os.FileMode { return mode | 0o200 },
		BeforeCommit: func(file *os.File, _ os.FileInfo) error {
			body, readErr := io.ReadAll(file)
			if readErr != nil {
				return readErr
			}
			if string(body) != "different" {
				return guardErr
			}
			return nil
		},
	})
	if !errors.Is(err, guardErr) {
		t.Fatalf("UpdateMode error = %v, want guard error", err)
	}
	info, statErr := os.Stat(target)
	testutil.FailErr(t, "stat unchanged", statErr)
	if info.Mode().Perm() != 0o444 {
		t.Fatalf("mode = %o, want unchanged 444", info.Mode().Perm())
	}
}

func TestUpdateModePreservesSpecialBits(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows does not expose Unix special mode bits")
	}
	root := t.TempDir()
	target := filepath.Join(root, "run.sh")
	testutil.FailErr(t, "seed target", os.WriteFile(target, []byte("#!/bin/sh\n"), 0o455))
	testutil.FailErr(t, "set special mode", os.Chmod(target, 0o455|os.ModeSetuid))
	_, err := fseffect.UpdateMode(fseffect.ModeUpdateRequest{
		Location: fseffect.Location{Root: root, Rel: "run.sh"},
		Update:   func(mode os.FileMode) os.FileMode { return mode | 0o200 },
	})
	testutil.FailErr(t, "update mode", err)
	info, err := os.Stat(target)
	testutil.FailErr(t, "stat target", err)
	if info.Mode().Perm() != 0o655 || info.Mode()&os.ModeSetuid == 0 {
		t.Fatalf("mode = %v, want 0655 with setuid", info.Mode())
	}
}

func TestUpdateModeRefusesSymlinkTargetAndParent(t *testing.T) {
	root := t.TempDir()
	outside := filepath.Join(t.TempDir(), "outside")
	testutil.FailErr(t, "seed outside", os.WriteFile(outside, []byte("safe"), 0o600))
	testutil.FailErr(t, "symlink target", os.Symlink(outside, filepath.Join(root, "out")))
	setMode := func(loc fseffect.Location) error {
		_, err := fseffect.UpdateMode(fseffect.ModeUpdateRequest{
			Location: loc,
			Update:   func(os.FileMode) os.FileMode { return 0o777 },
		})
		return err
	}
	if err := setMode(fseffect.Location{Root: root, Rel: "out"}); !errors.Is(err, fseffect.ErrSymlink) {
		t.Fatalf("UpdateMode symlink target error = %v, want ErrSymlink", err)
	}
	testutil.FailErr(t, "symlink parent", os.Symlink(filepath.Dir(outside), filepath.Join(root, "linkdir")))
	if err := setMode(fseffect.Location{Root: root, Rel: "linkdir/outside"}); !errors.Is(err, fseffect.ErrSymlink) {
		t.Fatalf("UpdateMode symlink parent error = %v, want ErrSymlink", err)
	}
	info, err := os.Stat(outside)
	testutil.FailErr(t, "stat outside", err)
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("outside mode = %o, want unchanged 600", info.Mode().Perm())
	}
}

func TestChownAppliesAndRefusesSymlinkTarget(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "owned.txt")
	testutil.FailErr(t, "seed target", os.WriteFile(target, []byte("x"), 0o644))
	testutil.FailErr(t, "Chown", fseffect.Chown(fseffect.Location{Root: root, Rel: "owned.txt"}, os.Getuid(), os.Getgid()))
	outside := filepath.Join(t.TempDir(), "outside")
	testutil.FailErr(t, "seed outside", os.WriteFile(outside, []byte("safe"), 0o600))
	testutil.FailErr(t, "symlink target", os.Symlink(outside, filepath.Join(root, "out")))
	if err := fseffect.Chown(fseffect.Location{Root: root, Rel: "out"}, os.Getuid(), os.Getgid()); !errors.Is(err, fseffect.ErrSymlink) {
		t.Fatalf("Chown symlink target error = %v, want ErrSymlink", err)
	}
}

func TestReplaceCommitsExactBytesAndMode(t *testing.T) {
	t.Parallel()
	target := filepath.Join(t.TempDir(), "run.sh")
	testutil.FailErr(t, "seed target", os.WriteFile(target, []byte("old\n"), 0o755))
	result, err := fseffect.Replace(fseffect.ReplaceRequest{
		Location: fseffect.PathLocation(target),
		Source:   bytes.NewReader([]byte("new\r\n🐺")), Mode: 0o755,
	})
	testutil.FailErr(t, "replace target", err)
	got, err := os.ReadFile(target)
	testutil.FailErr(t, "read replacement", err)
	info, err := os.Stat(target)
	testutil.FailErr(t, "stat replacement", err)
	if !bytes.Equal(got, []byte("new\r\n🐺")) || info.Mode().Perm() != 0o755 || result.Bytes != int64(len(got)) {
		t.Fatalf("replacement = %x mode=%v", got, info.Mode().Perm())
	}
}

func TestReplacePreservesCurrentMode(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows does not expose Unix special mode bits")
	}
	root := t.TempDir()
	target := filepath.Join(root, "run.sh")
	testutil.FailErr(t, "seed target", os.WriteFile(target, []byte("old\n"), 0o755))
	testutil.FailErr(t, "set target mode", os.Chmod(target, 0o755|os.ModeSetuid))
	_, err := fseffect.Replace(fseffect.ReplaceRequest{
		Location:     fseffect.Location{Root: root, Rel: "run.sh"},
		Source:       bytes.NewReader([]byte("new\n")),
		Mode:         0o600,
		PreserveMode: true,
	})
	testutil.FailErr(t, "replace target", err)
	info, err := os.Stat(target)
	testutil.FailErr(t, "stat target", err)
	if info.Mode().Perm() != 0o755 {
		t.Fatalf("mode = %v, want 0755", info.Mode())
	}
	if info.Mode()&os.ModeSetuid == 0 {
		t.Fatalf("mode = %v, want setuid", info.Mode())
	}
}

func TestReplaceRefusesFailedPrecommitWithoutChangingDestination(t *testing.T) {
	t.Parallel()
	target := filepath.Join(t.TempDir(), "document.txt")
	testutil.FailErr(t, "seed target", os.WriteFile(target, []byte("newer\n"), 0o644))
	sentinel := errors.New("conflict")
	_, err := fseffect.Replace(fseffect.ReplaceRequest{
		Location: fseffect.PathLocation(target),
		Source:   bytes.NewReader([]byte("stale\n")), Mode: 0o644,
		BeforeCommit: func(_ fseffect.Target, staged fseffect.Result) error {
			if staged.Bytes != int64(len("stale\n")) || len(staged.SHA256) != 64 {
				t.Fatalf("staged result = %+v", staged)
			}
			return sentinel
		},
	})
	if !errors.Is(err, sentinel) {
		t.Fatalf("error = %v, want conflict", err)
	}
	got, readErr := os.ReadFile(target)
	testutil.FailErr(t, "read conflicted destination", readErr)
	if string(got) != "newer\n" {
		t.Fatalf("conflict changed destination: %q", got)
	}
}

func TestReplaceSerializesConcurrentReplacementsOfSameTarget(t *testing.T) {
	root := t.TempDir()
	loc := fseffect.Location{Root: root, Rel: "shared.txt"}
	testutil.FailErr(t, "seed target", os.WriteFile(filepath.Join(root, "shared.txt"), []byte("base"), 0o600))

	var active int32
	entered := make(chan struct{})
	release := make(chan struct{})

	aDone := make(chan struct{})
	go func() {
		defer close(aDone)
		_, err := fseffect.Replace(fseffect.ReplaceRequest{
			Location: loc, Mode: 0o600,
			Source: bytes.NewReader([]byte("from-A")),
			BeforeCommit: func(fseffect.Target, fseffect.Result) error {
				if !atomic.CompareAndSwapInt32(&active, 0, 1) {
					t.Error("writer A entered its critical section concurrently with another writer")
				}
				close(entered)
				<-release
				atomic.StoreInt32(&active, 0)
				return nil
			},
		})
		testutil.FailErr(t, "writer A replace", err)
	}()

	<-entered // Writer A holds the target lock.

	bDone := make(chan struct{})
	go func() {
		defer close(bDone)
		_, err := fseffect.Replace(fseffect.ReplaceRequest{
			Location: loc, Mode: 0o600,
			Source: bytes.NewReader([]byte("from-B")),
			BeforeCommit: func(fseffect.Target, fseffect.Result) error {
				if !atomic.CompareAndSwapInt32(&active, 0, 1) {
					t.Error("writer B entered its critical section while writer A still held the target")
				}
				atomic.StoreInt32(&active, 0)
				return nil
			},
		})
		testutil.FailErr(t, "writer B replace", err)
	}()

	select {
	case <-bDone:
		t.Fatal("writer B completed its Replace before writer A released the target")
	case <-time.After(50 * time.Millisecond):
	}

	close(release)
	<-aDone
	<-bDone
}

func TestUpdateModeSerializesWithReplacement(t *testing.T) {
	root := t.TempDir()
	loc := fseffect.Location{Root: root, Rel: "shared.txt"}
	testutil.FailErr(t, "seed target", os.WriteFile(filepath.Join(root, "shared.txt"), []byte("base"), 0o600))

	replaceEntered := make(chan struct{})
	releaseReplace := make(chan struct{})
	replaceDone := make(chan error, 1)
	go func() {
		_, err := fseffect.Replace(fseffect.ReplaceRequest{
			Location: loc,
			Source:   bytes.NewReader([]byte("replacement")),
			Mode:     0o600,
			BeforeCommit: func(fseffect.Target, fseffect.Result) error {
				close(replaceEntered)
				<-releaseReplace
				return nil
			},
		})
		replaceDone <- err
	}()
	<-replaceEntered

	modeEntered := make(chan struct{})
	modeDone := make(chan error, 1)
	go func() {
		_, err := fseffect.UpdateMode(fseffect.ModeUpdateRequest{
			Location: loc,
			Update:   func(mode os.FileMode) os.FileMode { return mode | 0o044 },
			BeforeCommit: func(*os.File, os.FileInfo) error {
				close(modeEntered)
				return nil
			},
		})
		modeDone <- err
	}()

	select {
	case <-modeEntered:
		t.Fatal("UpdateMode entered before Replace released the target")
	case <-time.After(50 * time.Millisecond):
	}
	close(releaseReplace)
	testutil.FailErr(t, "replace", <-replaceDone)
	testutil.FailErr(t, "update mode", <-modeDone)
	info, err := os.Stat(filepath.Join(root, "shared.txt"))
	testutil.FailErr(t, "stat target", err)
	if info.Mode().Perm() != 0o644 {
		t.Fatalf("mode = %o, want 644", info.Mode().Perm())
	}
}

func TestReplaceCreatesIntermediateDirectoriesAtRequestedMode(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	_, err := fseffect.Replace(fseffect.ReplaceRequest{
		Location: fseffect.Location{Root: root, Rel: "private/state.yaml"},
		Source:   bytes.NewReader([]byte("secret: 1\n")), Mode: 0o600, DirMode: 0o700,
	})
	testutil.FailErr(t, "Replace", err)
	info, err := os.Stat(filepath.Join(root, "private"))
	testutil.FailErr(t, "stat intermediate directory", err)
	if info.Mode().Perm() != 0o700 {
		t.Fatalf("intermediate directory mode = %v, want 0700", info.Mode().Perm())
	}
}

func TestOpenReadAcceptsCapabilityRoot(t *testing.T) {
	root := t.TempDir()
	f, err := fseffect.OpenRead(fseffect.Location{Root: root, Rel: "."})
	testutil.FailErr(t, "open capability root", err)
	defer func() { _ = f.Close() }()
	info, err := f.Stat()
	testutil.FailErr(t, "stat capability root", err)
	if !info.IsDir() {
		t.Fatalf("capability root mode = %v", info.Mode())
	}
}
