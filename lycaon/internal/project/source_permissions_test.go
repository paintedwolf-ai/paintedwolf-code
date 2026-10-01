package project

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/textfile"
)

func TestMakeSourceEditableAddsOnlyOwnerWrite(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows exposes the read-only attribute rather than Unix mode bits")
	}
	p, root := writeTestProject(t, map[string]string{"script.sh": "#!/bin/sh\n"})
	abs := filepath.Join(root, "script.sh")
	testutil.FailErr(t, "make read only", os.Chmod(abs, 0o455))

	result, err := MakeSourceEditable(p, SourceMakeEditableRequest{
		RootID: "r1", Path: "script.sh", BaseSHA256: textfile.SHA256([]byte("#!/bin/sh\n")),
	})
	testutil.FailErr(t, "make editable", err)
	if result.PreviousMode != 0o455 || result.Mode != 0o655 || !result.Writable {
		t.Fatalf("result = %+v, want 0455 -> 0655 writable", result)
	}
	info, err := os.Stat(abs)
	testutil.FailErr(t, "stat result", err)
	if info.Mode().Perm() != 0o655 {
		t.Fatalf("mode = %o, want 655", info.Mode().Perm())
	}
	bytes, err := os.ReadFile(abs)
	testutil.FailErr(t, "read result", err)
	if string(bytes) != "#!/bin/sh\n" {
		t.Fatalf("content changed: %q", bytes)
	}
}

func TestMakeSourceEditableIsIdempotentForMatchingBytes(t *testing.T) {
	p, _ := writeTestProject(t, map[string]string{"note.txt": "same\n"})
	request := SourceMakeEditableRequest{
		RootID: "r1", Path: "note.txt", BaseSHA256: textfile.SHA256([]byte("same\n")),
	}
	first, err := MakeSourceEditable(p, request)
	testutil.FailErr(t, "first make editable", err)
	second, err := MakeSourceEditable(p, request)
	testutil.FailErr(t, "second make editable", err)
	if !first.Writable || !second.Writable || second.PreviousMode != second.Mode {
		t.Fatalf("idempotent results = first %+v second %+v", first, second)
	}
}

func TestMakeSourceEditableRejectsStaleBytes(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows exposes the read-only attribute rather than Unix mode bits")
	}
	p, root := writeTestProject(t, map[string]string{"note.txt": "current\n"})
	abs := filepath.Join(root, "note.txt")
	testutil.FailErr(t, "make read only", os.Chmod(abs, 0o444))

	_, err := MakeSourceEditable(p, SourceMakeEditableRequest{
		RootID: "r1", Path: "note.txt", BaseSHA256: textfile.SHA256([]byte("stale\n")),
	})
	if !errors.Is(err, ErrSourceWriteConflict) {
		t.Fatalf("error = %v, want ErrSourceWriteConflict", err)
	}
	info, statErr := os.Stat(abs)
	testutil.FailErr(t, "stat unchanged", statErr)
	if info.Mode().Perm() != 0o444 {
		t.Fatalf("mode = %o, want unchanged 444", info.Mode().Perm())
	}
}

func TestMakeSourceEditableRefusesSymlink(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink setup requires elevated privileges on some Windows hosts")
	}
	root := t.TempDir()
	out := filepath.Join(t.TempDir(), "outside.txt")
	testutil.FailErr(t, "seed target", os.WriteFile(out, []byte("safe\n"), 0o444))
	testutil.FailErr(t, "seed link", os.Symlink(out, filepath.Join(root, "alias.txt")))
	p := &Project{ID: "p1", Roots: []Root{{ID: "r1", Path: root, IsPrimary: true}}}
	_, err := MakeSourceEditable(p, SourceMakeEditableRequest{
		RootID: "r1", Path: "alias.txt", BaseSHA256: textfile.SHA256([]byte("safe\n")),
	})
	if !errors.Is(err, ErrSourcePathDenied) {
		t.Fatalf("error = %v, want ErrSourcePathDenied", err)
	}
}
