package sourceblob

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
)

// Vectors verified against `git hash-object` for both hash formats.
var gitOIDVectors = []struct {
	name    string
	content string
	sha1    string
	sha256  string
}{
	{
		name: "empty blob", content: "",
		sha1:   "e69de29bb2d1d6434b8b29ae775ad8c2e48c5391",
		sha256: "473a0f4c3be8a93681a267e3b1e9a7dcda1185436fe141f7749120a303721813",
	},
	{
		name: "hello newline", content: "hello\n",
		sha1:   "ce013625030ba8dba906f756967f9e9ca394464a",
		sha256: "2cf8d83d9ee29543b34a87727421fdecb7e3f3a183d337639025de576db9ebb4",
	},
}

func TestContentGitOIDsMatchGitHashObject(t *testing.T) {
	for _, vector := range gitOIDVectors {
		oids := ContentGitOIDs([]byte(vector.content))
		if oids.SHA1 != vector.sha1 || oids.SHA256 != vector.sha256 {
			t.Fatalf("%s: oids = %+v", vector.name, oids)
		}
	}
}

func TestPutDerivesGitOIDs(t *testing.T) {
	store := New(t.TempDir())
	content := []byte("hello\n")
	_, _, oids, err := store.Put(ContentSHA(content), content)
	testutil.FailErr(t, "store object", err)
	if oids.SHA1 != gitOIDVectors[1].sha1 || oids.SHA256 != gitOIDVectors[1].sha256 {
		t.Fatalf("stored oids = %+v", oids)
	}
	// A dedup hit answers with the same derived identity.
	_, _, again, err := store.Put(ContentSHA(content), content)
	testutil.FailErr(t, "re-store object", err)
	if again != oids {
		t.Fatalf("dedup oids = %+v, first store said %+v", again, oids)
	}
}

func TestPutFileStreamsTheSameGitOIDs(t *testing.T) {
	store := New(t.TempDir())
	dir := t.TempDir()
	path := filepath.Join(dir, "greeting.txt")
	testutil.FailErr(t, "write source", os.WriteFile(path, []byte("hello\n"), 0o644))
	got, err := store.PutFile(t.Context(), path)
	testutil.FailErr(t, "capture file", err)
	if got.GitOIDs.SHA1 != gitOIDVectors[1].sha1 || got.GitOIDs.SHA256 != gitOIDVectors[1].sha256 {
		t.Fatalf("streamed oids = %+v", got.GitOIDs)
	}
	if got.GitOIDs != ContentGitOIDs([]byte("hello\n")) {
		t.Fatal("streaming and in-memory derivations disagree")
	}
}
