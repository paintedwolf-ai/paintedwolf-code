package fseffect

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/fspath"
	"github.com/lycaon/lycaon/internal/testutil"
)

// lookalike follows the staging grammar but was written by the user.
const lookalike = ".notes.txt.0123456789abcdef01234567.tmp"

// AddedEntry returns the one entry of dir absent from before, so tests find
// the door's staging path from the filesystem rather than from the door.
func AddedEntry(t testing.TB, dir string, before ...string) string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	testutil.FailErr(t, "list staging directory", err)
	var added []string
	for _, entry := range entries {
		if !slices.Contains(before, entry.Name()) {
			added = append(added, entry.Name())
		}
	}
	if len(added) != 1 {
		t.Fatalf("expected one staged entry in %s, found %v", dir, added)
	}
	return filepath.Join(dir, added[0])
}

func TestReplaceStagingIsRegisteredOnlyForItsOwnEntry(t *testing.T) {
	root := t.TempDir()
	other := t.TempDir()
	testutil.FailErr(t, "write user lookalike", os.WriteFile(filepath.Join(root, lookalike), []byte("user"), 0o600))
	var staged string
	_, err := Replace(ReplaceRequest{
		Location: Location{Root: root, Rel: "notes.txt"}, Source: strings.NewReader("saved"), Mode: 0o644,
		ReviewStaged: func(Target, Result) error {
			staged = AddedEntry(t, root, lookalike)
			name := filepath.Base(staged)
			if !IsStaging(root, name) {
				t.Fatal("in-flight staging entry was not registered")
			}
			if IsStaging(root, lookalike) {
				t.Fatal("user file following the staging grammar was registered")
			}
			testutil.FailErr(t, "write same name elsewhere", os.WriteFile(filepath.Join(other, name), nil, 0o600))
			if IsStaging(other, name) {
				t.Fatal("same-named entry in another directory was registered")
			}
			return nil
		},
	})
	testutil.FailErr(t, "replace", err)
	if _, err := os.Lstat(staged); !os.IsNotExist(err) {
		t.Fatalf("staging entry survived commit: %v", err)
	}
	if !IsStaging(root, filepath.Base(staged)) {
		t.Fatal("retired staging entry left the registry before late observers could see it")
	}
}

func TestConditionalRemoveQuarantineIsRegistered(t *testing.T) {
	root := t.TempDir()
	testutil.FailErr(t, "seed target", os.WriteFile(filepath.Join(root, "notes.txt"), []byte("old"), 0o600))
	err := Remove(RemoveRequest{
		Location: Location{Root: root, Rel: "notes.txt"},
		BeforeCommit: func(Target) error {
			quarantine := AddedEntry(t, root)
			if !IsStaging(root, filepath.Base(quarantine)) {
				t.Fatal("in-flight removal quarantine was not registered")
			}
			return nil
		},
	})
	testutil.FailErr(t, "conditional remove", err)
}

func TestRetiredStagingExpiresAfterRetention(t *testing.T) {
	root := t.TempDir()
	identity, err := fspath.EntryIdentity(root + string(os.PathSeparator) + ".")
	testutil.FailErr(t, "identify directory", err)
	expired := holdStaging(identity, ".expired.tmp")
	expired.retire()
	staging.Lock()
	expired.entry.retired = time.Now().Add(-2 * stagingRetention)
	staging.Unlock()
	if IsStaging(root, ".expired.tmp") {
		t.Fatal("staging entry outlived its retention window")
	}
	abandoned := holdStaging(identity, ".abandoned.tmp")
	abandoned.abandon()
	if IsStaging(root, ".abandoned.tmp") {
		t.Fatal("an abandoned name the door never created stayed registered")
	}
}
