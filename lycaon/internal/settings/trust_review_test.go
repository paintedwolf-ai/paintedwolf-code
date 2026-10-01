package settings

import (
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/project"
)

func TestTrustReviewDeduplicatesFilesAndPreservesRootIdentity(t *testing.T) {
	first, second := t.TempDir(), t.TempDir()
	for _, root := range []string{first, second} {
		writeTrustFixture(t, root, filepath.Join(".paintedwolf", "extensions.yaml"), "format: 1\nsuggest:\n  - id: acme/tool\ndisabled:\n  - acme/other\n")
	}
	p := project.Project{Roots: []project.Root{{ID: "first", Label: "First", Path: first}, {ID: "second", Label: "Second", Path: second}}}
	read := SeenRecordsFor(p, scanTrustManifest(t, []string{first, second}))
	added := TrustChanges(nil, read)
	if len(added) != 2 || len(added[0].SurfaceIds) != 2 || added[0].RootID == added[1].RootID {
		t.Fatalf("file/root identity = %+v", added)
	}
	p.Roots = p.Roots[1:]
	next := SeenRecordsFor(p, scanTrustManifest(t, []string{second}))
	removed := TrustChanges(read, next)
	if len(removed) != 1 || removed[0].RootID != "first" || removed[0].RootLabel != "First" || removed[0].Kind != "removed" || removed[0].Before == "" {
		t.Fatalf("detached root = %+v", removed)
	}
}

func TestTrustReviewDistinguishesEmptyFileFromAbsence(t *testing.T) {
	root := t.TempDir()
	writeTrustFixture(t, root, "AGENTS.md", "")
	p := projectAt(root)
	snapshot := SeenRecordsFor(p, scanTrustManifest(t, []string{root}))
	added := TrustChanges(nil, snapshot)
	removed := TrustChanges(snapshot, nil)
	if len(added) != 1 || added[0].Kind != "added" || len(removed) != 1 || removed[0].Kind != "removed" {
		t.Fatalf("empty file transitions: %+v %+v", added, removed)
	}
}
