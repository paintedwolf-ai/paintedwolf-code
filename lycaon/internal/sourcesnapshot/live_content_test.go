package sourcesnapshot

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/sourceblob"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestReusedGenerationAcceptsIdenticalRewrite(t *testing.T) {
	store := openSnapshotStore(t)
	root := t.TempDir()
	coverRoot(t, root)
	writeSource(t, root, "main.go", "package main\n")
	before, err := store.EnsurePath(t.Context(), root, VerifyStat)
	testutil.FailErr(t, "publish original", err)
	entry := lookupEntry(t, store, before, "main.go")
	path := filepath.Join(root, "main.go")
	modified := time.Unix(0, entry.ModifiedNS).Add(time.Hour)
	testutil.FailErr(t, "touch identical content", os.Chtimes(path, modified, modified))
	notifyChange(root, "main.go")
	after, err := store.EnsurePath(t.Context(), root, VerifyStat)
	testutil.FailErr(t, "publish identical rewrite", err)
	if before.ID != after.ID {
		t.Fatal("identical content created another generation")
	}
	for _, paths := range [][]string{nil, {"main.go"}} {
		moved, err := store.MovedSince(t.Context(), after.ID, paths)
		testutil.FailErr(t, "check identical rewrite", err)
		if len(moved) != 0 {
			t.Fatalf("identical content reported moved: %v", moved)
		}
	}
	raw, err := store.Bytes(t.Context(), entry)
	testutil.FailErr(t, "read original content after touch", err)
	if string(raw) != "package main\n" {
		t.Fatalf("read content = %q", raw)
	}
	writeSource(t, root, "main.go", "package next\n")
	moved, err := store.MovedSince(t.Context(), after.ID, nil)
	testutil.FailErr(t, "check changed content", err)
	if len(moved) != 1 || moved[0] != "main.go" {
		t.Fatalf("changed content reported moved: %v", moved)
	}
}

func TestLiveVerificationRetainsIdentityAndFileBoundaries(t *testing.T) {
	for _, identity := range []Identity{IdentityHashed, IdentityIndex, IdentityStat} {
		t.Run(string(identity), func(t *testing.T) {
			store := openSnapshotStore(t)
			root := t.TempDir()
			writeSource(t, root, "main.go", "package main\n")
			snapshot, err := store.EnsurePath(t.Context(), root, VerifyStat)
			testutil.FailErr(t, "publish source", err)
			entry := lookupEntry(t, store, snapshot, "main.go")
			entry.Identity = identity
			switch identity {
			case IdentityHashed:
				entry.GitOID = ""
			case IdentityIndex:
				entry.SHA256 = ""
			case IdentityStat:
				entry.SHA256, entry.GitOID = "", ""
			}
			path := filepath.Join(root, "main.go")
			modified := time.Unix(0, entry.ModifiedNS).Add(time.Hour)
			testutil.FailErr(t, "touch source", os.Chtimes(path, modified, modified))
			_, readErr := readLiveVerified(t.Context(), entry)
			digest, digestErr := store.Digest(t.Context(), entry)
			wantMatch := identity != IdentityStat
			if entry.liveContentMatches(t.Context()) != wantMatch || (readErr == nil) != wantMatch || (digestErr == nil) != wantMatch {
				t.Fatalf("touch verification: read=%v digest=%v want match=%v", readErr, digestErr, wantMatch)
			}
			if wantMatch && digest != sourceblob.ContentSHA([]byte("package main\n")) {
				t.Fatalf("digest = %q", digest)
			}
			writeSource(t, root, "main.go", "package next\n")
			if entry.liveContentMatches(t.Context()) {
				t.Fatal("changed bytes accepted")
			}
			if _, err := readLiveVerified(t.Context(), entry); err == nil {
				t.Fatal("read accepted changed bytes")
			}
			if _, err := store.Digest(t.Context(), entry); entry.SHA256 == "" && err == nil {
				t.Fatal("digest accepted changed bytes")
			}
			testutil.FailErr(t, "remove source", os.Remove(path))
			writeSource(t, root, "target.go", "package main\n")
			testutil.FailErr(t, "replace source with symlink", os.Symlink(filepath.Join(root, "target.go"), path))
			if entry.liveContentMatches(t.Context()) {
				t.Fatal("symlink accepted as the original regular file")
			}
		})
	}
}

// A hashed file's bytes come from a digest-checked read; Git is the fallback
// once the file moved on, and the source for a file only Git identified.
func TestBytesAsksGitOnlyWhenTheFileCannotAnswer(t *testing.T) {
	store := openSnapshotStore(t)
	root := t.TempDir()
	writeSource(t, root, "main.go", "package main\n")
	snapshot, err := store.EnsurePath(t.Context(), root, VerifyStat)
	testutil.FailErr(t, "publish", err)
	entry := lookupEntry(t, store, snapshot, "main.go")
	if entry.SHA256 == "" || entry.GitOID == "" {
		t.Fatalf("entry = %+v, want a hashed file with a Git identity", entry)
	}
	gitReads := 0
	store.gitObject = func(context.Context, Entry) ([]byte, error) {
		gitReads++
		return []byte("from git"), nil
	}
	raw, err := store.Bytes(t.Context(), entry)
	testutil.FailErr(t, "read hashed file", err)
	if string(raw) != "package main\n" || gitReads != 0 {
		t.Fatalf("hashed read = %q with %d Git reads", raw, gitReads)
	}
	writeSource(t, root, "main.go", "package next\n")
	raw, err = store.Bytes(t.Context(), entry)
	testutil.FailErr(t, "read moved file", err)
	if string(raw) != "from git" || gitReads != 1 {
		t.Fatalf("moved read = %q with %d Git reads", raw, gitReads)
	}
	store.gitObject = func(context.Context, Entry) ([]byte, error) { return nil, errChanged }
	if _, err := store.Bytes(t.Context(), entry); !errors.Is(err, ErrContentUnavailable) {
		t.Fatalf("moved read with no object = %v", err)
	}
}
