package sourceledger

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/projectroot"
	"github.com/lycaon/lycaon/internal/testutil"
)

// outsideFixture makes a host folder outside the scratch roots and returns its
// outside-namespace path.
func outsideFixture(t *testing.T) (string, string) {
	t.Helper()
	home, err := os.UserHomeDir()
	testutil.FailErr(t, "locate the home folder", err)
	dir, err := os.MkdirTemp(home, ".pw-outside-locations-")
	testutil.FailErr(t, "make an outside folder", err)
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	canonical, err := filepath.EvalSymlinks(dir)
	testutil.FailErr(t, "canonicalize the outside folder", err)
	rel, ok := projectroot.OutsideRel(canonical)
	if !ok {
		t.Skip("the outside namespace covers host paths with a single root")
	}
	return canonical, rel
}

func TestLocationsAnchorAtRepositoryOrFolder(t *testing.T) {
	store, ctx := openLedger(t)
	outside := outsideRoot(t, store, ctx)
	insertChat(t, store, ctx, "chat", "")
	dir, rel := outsideFixture(t)
	testutil.FailErr(t, "make a repository", os.MkdirAll(filepath.Join(dir, "site", ".git"), 0o755))
	testutil.FailErr(t, "make a folder", os.MkdirAll(filepath.Join(dir, "notes"), 0o755))

	recordOutside(t, store, ctx, outside, "chat", rel+"/site/src/app.ts", "app\n")
	recordOutside(t, store, ctx, outside, "chat", rel+"/site/README.md", "readme\n")
	recordOutside(t, store, ctx, outside, "chat", rel+"/notes/todo.txt", "todo\n")
	locations, err := store.ChatLocations(ctx, "chat")
	testutil.FailErr(t, "list locations", err)
	if len(locations) != 2 {
		t.Fatalf("locations = %+v, want the repository and the folder", locations)
	}
	byAnchor := map[string]ChatLocation{}
	for _, location := range locations {
		byAnchor[location.Anchor] = location
	}
	if byAnchor[rel+"/site"].Kind != ChatLocationRepository || byAnchor[rel+"/notes"].Kind != ChatLocationFolder {
		t.Fatalf("anchors = %+v", byAnchor)
	}
	admitted, err := store.OutsidePathAdmitted(ctx, "p1", rel+"/site/untouched.go")
	testutil.FailErr(t, "admit an untouched sibling", err)
	if !admitted {
		t.Fatal("a file the location covers was not admitted")
	}
	admitted, err = store.OutsidePathAdmitted(ctx, "p1", rel+"/elsewhere.txt")
	testutil.FailErr(t, "refuse an uncovered path", err)
	if admitted {
		t.Fatal("a path no location covers was admitted")
	}
}

func TestHiddenLocationReturnsWithANewChange(t *testing.T) {
	store, ctx := openLedger(t)
	outside := outsideRoot(t, store, ctx)
	insertChat(t, store, ctx, "chat", "")
	_, rel := outsideFixture(t)
	recordOutside(t, store, ctx, outside, "chat", rel+"/a.txt", "a\n")
	locations, err := store.ChatLocations(ctx, "chat")
	testutil.FailErr(t, "list locations", err)
	if len(locations) != 1 {
		t.Fatalf("locations = %+v", locations)
	}
	testutil.FailErr(t, "hide the location", store.HideChatLocation(ctx, "p1", locations[0].ID, true))
	hidden, err := store.ChatLocation(ctx, "p1", locations[0].ID)
	testutil.FailErr(t, "read the hidden location", err)
	if !hidden.Hidden {
		t.Fatal("hiding did not hide the location")
	}
	admitted, err := store.OutsidePathAdmitted(ctx, "p1", rel+"/a.txt")
	testutil.FailErr(t, "admit a hidden location's file", err)
	if !admitted {
		t.Fatal("hiding revoked the person's access")
	}
	recordOutside(t, store, ctx, outside, "chat", rel+"/b.txt", "b\n")
	shown, err := store.ChatLocation(ctx, "p1", locations[0].ID)
	testutil.FailErr(t, "read the location again", err)
	if shown.Hidden {
		t.Fatal("a new change left the location hidden")
	}
}

func TestLocationsBelongToTheRootChat(t *testing.T) {
	store, ctx := openLedger(t)
	outside := outsideRoot(t, store, ctx)
	insertChat(t, store, ctx, "chat", "")
	insertChat(t, store, ctx, "worker", "chat")
	insertChat(t, store, ctx, "other", "")
	_, rel := outsideFixture(t)
	recordOutside(t, store, ctx, outside, "worker", rel+"/w.txt", "w\n")
	for chat, want := range map[string]int{"chat": 1, "worker": 1, "other": 0} {
		locations, err := store.ChatLocations(ctx, chat)
		testutil.FailErr(t, "list locations of "+chat, err)
		if len(locations) != want {
			t.Fatalf("%s locations = %+v, want %d", chat, locations, want)
		}
	}
}

func TestTrackChatFileRecordsAReadWithoutAnEffect(t *testing.T) {
	store, ctx := openLedger(t)
	outside := outsideRoot(t, store, ctx)
	insertChat(t, store, ctx, "chat", "")
	_, rel := outsideFixture(t)
	tracked, err := store.TrackChatFile(ctx, TrackInput{ProjectID: "p1", RootID: outside, Path: rel + "/read.txt", Content: []byte("read\n")}, "chat", ChatFileRead)
	testutil.FailErr(t, "track a read", err)
	refs, err := store.queries.ListSourceChatFiles(ctx, "chat")
	testutil.FailErr(t, "list references", err)
	if len(refs) != 1 || refs[0].FileID != tracked.FileID || refs[0].Read != 1 || refs[0].Changed != 0 {
		t.Fatalf("references = %+v", refs)
	}
	var effects int
	testutil.FailErr(t, "count effects", store.sqlDB.QueryRowContext(ctx, `SELECT count(*) FROM source_effects WHERE file_id = ?`, tracked.FileID).Scan(&effects))
	if effects != 0 {
		t.Fatalf("a read recorded %d effects", effects)
	}
}

func TestLocationRetainsItsObservedDirectory(t *testing.T) {
	store, ctx := openLedger(t)
	outside := outsideRoot(t, store, ctx)
	insertChat(t, store, ctx, "chat", "")
	dir, rel := outsideFixture(t)
	anchor := filepath.Join(dir, "notes")
	testutil.FailErr(t, "make observed directory", os.Mkdir(anchor, 0o700))
	recordOutside(t, store, ctx, outside, "chat", rel+"/notes/readme.txt", "first")
	location, found, err := store.ChatLocationCovering(ctx, "chat", rel+"/notes/readme.txt")
	testutil.FailErr(t, "find observed directory", err)
	if !found || !location.Available() {
		t.Fatalf("expected an available location: %+v", location)
	}
	testutil.FailErr(t, "move the original directory", os.Rename(anchor, anchor+"-old"))
	testutil.FailErr(t, "make replacement at the same path", os.Mkdir(anchor, 0o700))
	if location.Available() {
		t.Fatal("the replacement inherited the recorded directory identity")
	}
	admitted, err := store.OutsidePathAdmitted(ctx, "p1", rel+"/notes/new.txt")
	testutil.FailErr(t, "check the replacement directory", err)
	if admitted {
		t.Fatal("the replacement granted access to unobserved files")
	}
	recordOutside(t, store, ctx, outside, "chat", rel+"/notes/new.txt", "second")
	replacement, found, err := store.ChatLocationCovering(ctx, "chat", rel+"/notes/new.txt")
	testutil.FailErr(t, "find the newly observed directory", err)
	if !found || replacement.ID == location.ID || !replacement.Available() {
		t.Fatalf("a new reference did not admit the replacement: %+v", replacement)
	}
}
