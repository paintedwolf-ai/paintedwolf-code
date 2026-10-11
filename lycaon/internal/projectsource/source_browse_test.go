package projectsource

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/repochange"
	"github.com/lycaon/lycaon/internal/settingsoverlay"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/watchfd"
)

func browseFixtureProject(t *testing.T) (*Project, string) {
	t.Helper()
	dir := t.TempDir()
	for _, sub := range []string{"src", "docs", ".github", ".git", settingsoverlay.DirName()} {
		if err := os.MkdirAll(filepath.Join(dir, sub), 0o755); err != nil {
			testutil.FailErr(t, "mkdir fixture", err)
		}
	}
	for _, f := range []string{"README.md", "zeta.go", ".env", filepath.Join("src", "main.go")} {
		if err := os.WriteFile(filepath.Join(dir, f), []byte("x"), 0o644); err != nil {
			testutil.FailErr(t, "write fixture", err)
		}
	}
	p := &Project{
		ID:    "p1",
		Roots: []Root{{ID: "r1", Path: dir, IsPrimary: true}},
	}
	return p, dir
}

func TestBrowseProjectSourceListsSortedDirsFirst(t *testing.T) {
	p, _ := browseFixtureProject(t)

	listing, err := BrowseProjectSource(p, "", "")
	if err != nil {
		testutil.FailErr(t, "browse root", err)
	}
	if listing.RootID != "r1" || listing.Dir != "." {
		t.Fatalf("listing addressing = %q %q, want r1 .", listing.RootID, listing.Dir)
	}
	got := make([]string, 0, len(listing.Entries))
	for _, e := range listing.Entries {
		name := e.Name
		if e.IsDir {
			name += "/"
		}
		got = append(got, name)
	}
	overlay := settingsoverlay.DirName() + "/"
	want := []string{".git/", ".github/", overlay, "docs/", "src/", ".env", "README.md", "zeta.go"}
	if len(got) != len(want) {
		t.Fatalf("entries = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("entries = %v, want %v", got, want)
		}
	}
}

func TestBrowseProjectSourceSubdir(t *testing.T) {
	p, _ := browseFixtureProject(t)

	listing, err := BrowseProjectSource(p, "r1", "src")
	if err != nil {
		testutil.FailErr(t, "browse subdir", err)
	}
	if listing.Dir != "src" || len(listing.Entries) != 1 || listing.Entries[0].Name != "main.go" || listing.Entries[0].IsDir {
		t.Fatalf("src listing = %+v", listing)
	}
}

func TestBrowseProjectSourceIncludesGitAndIgnoredDirectories(t *testing.T) {
	p, root := browseFixtureProject(t)
	for _, dir := range []string{".git/refs/heads", ".task", "ignored"} {
		testutil.FailErr(t, "create human-visible directory", os.MkdirAll(filepath.Join(root, dir), 0o755))
		testutil.FailErr(t, "write human-visible file", os.WriteFile(filepath.Join(root, dir, "entry"), []byte("visible\n"), 0o644))
	}
	testutil.FailErr(t, "write ignore rules", os.WriteFile(filepath.Join(root, ".gitignore"), []byte(".task/\nignored/\n"), 0o644))
	for _, dir := range []string{".git/refs/heads", ".task", "ignored"} {
		listing, err := BrowseProjectSource(p, "r1", dir)
		testutil.FailErr(t, "browse direct directory", err)
		if len(listing.Entries) != 1 || listing.Entries[0].Name != "entry" {
			t.Fatalf("directory %q: %+v", dir, listing)
		}
	}
}

func TestSourceProjectionInvalidatesOnlyAffectedAncestors(t *testing.T) {
	p, rootPath := browseFixtureProject(t)
	projection := &sourceDirectoryProjection{
		listings: make(map[sourceProjectionKey]projectedSourceListing),
		flights:  make(map[sourceProjectionKey]*sourceProjectionFlight),
	}
	root, err := resolveSourceBrowseRoot(p, "r1")
	testutil.FailErr(t, "resolve root", err)
	workspaceID := p.WorkspaceID()
	rootKey := sourceProjectionKey{workspaceID: workspaceID, rootID: "r1", rootPath: rootPath, dir: "."}
	srcKey := sourceProjectionKey{workspaceID: workspaceID, rootID: "r1", rootPath: rootPath, dir: "src"}
	_, err = projection.get(rootKey, root)
	testutil.FailErr(t, "browse projected root", err)
	_, err = projection.get(srcKey, root)
	testutil.FailErr(t, "browse projected src", err)

	testutil.FailErr(t, "write new source", os.WriteFile(filepath.Join(rootPath, "src", "new.go"), []byte("x"), 0o644))
	projection.invalidate(repochange.Event{
		ProjectDir: rootPath,
		Kind:       repochange.WorktreeChanged,
		Paths:      []string{"src/new.go"},
	})
	listing, err := projection.get(srcKey, root)
	testutil.FailErr(t, "refresh projected src", err)
	found := false
	for _, entry := range listing.Entries {
		found = found || entry.Name == "new.go"
	}
	if !found {
		t.Fatalf("refreshed listing = %+v, want new.go", listing.Entries)
	}
}

func TestSourceProjectionUnwatchedReadsFreshMembership(t *testing.T) {
	p, rootPath := browseFixtureProject(t)
	projection := &sourceDirectoryProjection{
		listings: make(map[sourceProjectionKey]projectedSourceListing),
		flights:  make(map[sourceProjectionKey]*sourceProjectionFlight),
	}
	root, err := resolveSourceBrowseRoot(p, "r1")
	testutil.FailErr(t, "resolve root", err)
	key := sourceProjectionKey{
		workspaceID: p.WorkspaceID(), rootID: "r1", rootPath: rootPath, dir: ".",
	}
	_, err = projection.get(key, root)
	testutil.FailErr(t, "browse projected root", err)
	testutil.FailErr(t, "write unwatched source", os.WriteFile(filepath.Join(rootPath, "late.go"), []byte("x"), 0o644))

	listing, err := projection.get(key, root)
	testutil.FailErr(t, "browse unwatched directory again", err)
	found := false
	for _, entry := range listing.Entries {
		found = found || entry.Name == "late.go"
	}
	if !found {
		t.Fatalf("fresh listing = %+v, want late.go", listing.Entries)
	}
}

// A watched directory's listing is projected once and served from the cache.
func TestSourceProjectionCachesWatchedMembership(t *testing.T) {
	p, rootPath := browseFixtureProject(t)
	repochange.ResetWatchersForTest()
	t.Cleanup(repochange.ResetWatchersForTest)
	repochange.EnsureRoot(t.Context(), rootPath)
	projection := &sourceDirectoryProjection{
		listings: make(map[sourceProjectionKey]projectedSourceListing),
		flights:  make(map[sourceProjectionKey]*sourceProjectionFlight),
	}
	root, err := resolveSourceBrowseRoot(p, "r1")
	testutil.FailErr(t, "resolve root", err)
	key := sourceProjectionKey{
		workspaceID: p.WorkspaceID(), rootID: "r1", rootPath: rootPath, dir: ".",
	}
	first, err := projection.get(key, root)
	testutil.FailErr(t, "browse watched root", err)
	if !first.WatchComplete {
		t.Fatal("the watched root reported no watch coverage")
	}
	if _, ok := projection.listings[key]; !ok {
		t.Fatal("a watched listing was not kept as projected membership")
	}
	second, err := projection.get(key, root)
	testutil.FailErr(t, "browse watched root again", err)
	if len(second.Entries) != len(first.Entries) || !second.WatchComplete {
		t.Fatalf("cached listing = %+v, want the projected %+v", second, first)
	}
}

// Listing freshness follows its directory watch.
func TestSourceListingReportsWatchCoveragePerDirectory(t *testing.T) {
	p, rootPath := browseFixtureProject(t)
	repochange.ResetWatchersForTest()
	t.Cleanup(repochange.ResetWatchersForTest)
	repochange.EnsureRoot(t.Context(), rootPath)

	root, err := resolveSourceBrowseRoot(p, "r1")
	testutil.FailErr(t, "resolve root", err)

	// Ensure registers the root itself; children arrive with a catalog seed.
	rootListing, _, _, err := observeSourceListing(root, p.WorkspaceID(), ".")
	testutil.FailErr(t, "observe root listing", err)
	if !rootListing.WatchComplete {
		t.Fatal("the watched root directory reported no coverage, so its listing would poll forever")
	}

	unseeded, _, _, err := observeSourceListing(root, p.WorkspaceID(), "src")
	testutil.FailErr(t, "observe unseeded subdirectory", err)
	// A recursive root stream already sees writes in an unseeded directory; a
	// per-directory backend sees nothing there until the catalog seeds it.
	if unseeded.WatchComplete != watchfd.Recursive {
		t.Fatalf("unseeded directory watch_complete = %v, want %v for this backend",
			unseeded.WatchComplete, watchfd.Recursive)
	}

	repochange.SeedWatch(t.Context(), rootPath, []repochange.WatchDirectory{
		{Path: filepath.Join(rootPath, "src"), EntryCount: 1},
	})
	seeded, _, _, err := observeSourceListing(root, p.WorkspaceID(), "src")
	testutil.FailErr(t, "observe seeded subdirectory", err)
	if !seeded.WatchComplete {
		t.Fatal("a seeded directory still asked to be polled")
	}
}

func TestBrowseProjectSourceAllowsDotsInsideComponent(t *testing.T) {
	p, root := browseFixtureProject(t)
	testutil.FailErr(t, "mkdir dotted", os.MkdirAll(filepath.Join(root, "cache..snapshot"), 0o755))
	listing, err := BrowseProjectSource(p, "r1", "cache..snapshot")
	testutil.FailErr(t, "browse dotted component", err)
	if listing.Dir != "cache..snapshot" {
		t.Fatalf("dir = %q", listing.Dir)
	}
}

func TestBrowseProjectSourceJail(t *testing.T) {
	p, _ := browseFixtureProject(t)

	cases := []struct {
		name    string
		rootID  string
		dir     string
		wantErr error
	}{
		{"absolute dir denied", "", "/etc", ErrSourcePathDenied},
		{"traversal denied", "", "../outside", ErrSourcePathDenied},
		{"embedded traversal denied", "", "src/../../outside", ErrSourcePathDenied},
		{"missing dir", "", "no-such-dir", ErrSourceNotFound},
		{"file is not a dir", "", "README.md", ErrSourceNotFound},
		{"unknown root", "r-unknown", "", ErrSourceNotFound},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := BrowseProjectSource(p, tc.rootID, tc.dir)
			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("err = %v, want %v", err, tc.wantErr)
			}
		})
	}
}

func TestBrowseProjectSourceNoRoots(t *testing.T) {
	if _, err := BrowseProjectSource(&Project{ID: "p"}, "", ""); !errors.Is(err, ErrSourceNoRoot) {
		t.Fatalf("err = %v, want ErrSourceNoRoot", err)
	}
	if _, err := BrowseProjectSource(nil, "", ""); !errors.Is(err, ErrSourceNoRoot) {
		t.Fatalf("nil project err = %v, want ErrSourceNoRoot", err)
	}
	if _, err := BrowseProjectSource(&Project{ID: "p", Roots: []Root{{ID: "root", Path: t.TempDir()}}}, "", ""); !errors.Is(err, ErrSourceNoRoot) {
		t.Fatalf("root without primary err = %v, want ErrSourceNoRoot", err)
	}
}

func TestBrowseProjectSourceSymlinkEscapeDenied(t *testing.T) {
	p, dir := browseFixtureProject(t)
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(dir, "escape")); err != nil {
		testutil.FailErr(t, "symlink fixture", err)
	}

	if _, err := BrowseProjectSource(p, "", "escape"); !errors.Is(err, ErrSourcePathDenied) {
		t.Fatalf("symlink escape err = %v, want ErrSourcePathDenied", err)
	}
}

func TestBrowseProjectSourceSymlinkClassifiedByTarget(t *testing.T) {
	p, dir := browseFixtureProject(t)
	if err := os.Symlink(filepath.Join(dir, "src"), filepath.Join(dir, "src-link")); err != nil {
		testutil.FailErr(t, "symlink fixture", err)
	}
	if err := os.Symlink(filepath.Join(dir, "missing-target"), filepath.Join(dir, "dangling")); err != nil {
		testutil.FailErr(t, "dangling symlink fixture", err)
	}

	listing, err := BrowseProjectSource(p, "", "")
	if err != nil {
		testutil.FailErr(t, "browse with symlinks", err)
	}
	var sawLinkDir, sawDangling bool
	for _, e := range listing.Entries {
		if e.Name == "src-link" {
			sawLinkDir = e.IsDir
		}
		if e.Name == "dangling" {
			sawDangling = true
		}
	}
	if !sawLinkDir {
		t.Fatal("src-link should be listed as a directory (classified by target)")
	}
	if sawDangling {
		t.Fatal("dangling symlink should be dropped from the listing")
	}
}

func TestSourceBrowseOmitsRegisteredPrivateStage(t *testing.T) {
	p, root := browseFixtureProject(t)
	stage := filepath.Join(root, ".paintedwolf-copy-owned")
	testutil.FailErr(t, "create private stage", os.Mkdir(stage, 0o700))
	release := repochange.HoldPrivateTree(stage)
	defer release()
	listing, err := BrowseProjectSource(p, "r1", "")
	testutil.FailErr(t, "browse active operation", err)
	for _, entry := range listing.Entries {
		if entry.Name == filepath.Base(stage) {
			t.Fatal("private stage appeared in file tree")
		}
	}
}
