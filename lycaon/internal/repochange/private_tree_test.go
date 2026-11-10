package repochange

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/configdir"
	"github.com/lycaon/lycaon/internal/fseffect"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestPrivateTreeSuppressesOnlyItsOwnWatcherChanges(t *testing.T) {
	root := t.TempDir()
	stage := filepath.Join(root, "staging")
	release := HoldPrivateTree(stage)
	defer release()
	var got []string
	unbind := RegisterObserver(func(_ context.Context, event Event) { got = append(got, event.Paths...) })
	defer unbind()
	Notify(t.Context(), Event{ProjectDir: root, Kind: WorktreeChanged, Source: SourceWatcher, Paths: []string{"staging", "staging/child", "staging-other/file", "source.go"}})
	if len(got) != 2 || got[0] != "staging-other/file" || got[1] != "source.go" {
		t.Fatalf("observed paths=%v", got)
	}
	if !IsPrivatePath(filepath.Join(stage, "child")) || IsPrivatePath(filepath.Join(root, "staging-other")) {
		t.Fatal("private tree scope escaped its path boundary")
	}
}

func TestInFlightSaveStagingNeverReachesObservers(t *testing.T) {
	root := t.TempDir()
	// The user's file follows the staging grammar; only the door's entry is private.
	lookalike := ".notes.txt.0123456789abcdef01234567.tmp"
	testutil.FailErr(t, "write user lookalike", os.WriteFile(filepath.Join(root, lookalike), []byte("user"), 0o600))
	var got []string
	unbind := RegisterObserver(func(_ context.Context, event Event) { got = append(got, event.Paths...) })
	defer unbind()
	var staged string
	filter := NewPrivateDirectoryFilter(root)
	_, err := fseffect.Replace(fseffect.ReplaceRequest{
		Location: fseffect.Location{Root: root, Rel: "notes.txt"}, Source: strings.NewReader("saved"), Mode: 0o644,
		ReviewStaged: func(fseffect.Target, fseffect.Result) error {
			staged = addedEntry(t, root, lookalike)
			name := filepath.Base(staged)
			if !filter.Contains(name) || !IsPrivatePath(filepath.Join(root, name)) {
				t.Fatal("listing filters admitted the in-flight staging entry")
			}
			if filter.Contains(lookalike) || IsPrivatePath(filepath.Join(root, lookalike)) {
				t.Fatal("listing filters hid the user's lookalike file")
			}
			Notify(t.Context(), Event{ProjectDir: root, Kind: WorktreeChanged, Source: SourceWatcher, Paths: []string{name, lookalike}})
			return nil
		},
	})
	testutil.FailErr(t, "save", err)
	// The watcher reports the staging entry's removal after the commit.
	Notify(t.Context(), Event{ProjectDir: root, Kind: WorktreeChanged, Source: SourceWatcher, Paths: []string{filepath.Base(staged), "notes.txt"}})
	if slices.Contains(got, filepath.Base(staged)) || !slices.Contains(got, lookalike) || !slices.Contains(got, "notes.txt") {
		t.Fatalf("observed paths=%v staging=%q", got, filepath.Base(staged))
	}
}

// addedEntry finds the save's staging entry from the directory listing.
func addedEntry(t *testing.T, dir string, before ...string) string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	testutil.FailErr(t, "list save directory", err)
	for _, entry := range entries {
		if !slices.Contains(before, entry.Name()) {
			return filepath.Join(dir, entry.Name())
		}
	}
	t.Fatal("save staged no entry")
	return ""
}

func TestPrivateDirectoryFilterResolvesAliases(t *testing.T) {
	realRoot := t.TempDir()
	aliasParent := t.TempDir()
	alias := filepath.Join(aliasParent, "alias")
	if err := os.Symlink(realRoot, alias); err != nil {
		t.Fatalf("create root alias: %v", err)
	}
	release := HoldPrivateTree(filepath.Join(realRoot, "private"))
	defer release()
	filter := NewPrivateDirectoryFilter(alias)
	if !filter.Contains("private") || filter.Contains("visible") {
		t.Fatal("private directory filter did not preserve canonical alias identity")
	}
}

func TestPrivateDirectoryFilterReadsConcurrentRegistrations(t *testing.T) {
	root := t.TempDir()
	filter := NewPrivateDirectoryFilter(root)
	if filter.Contains("private") {
		t.Fatal("unregistered path reported private")
	}
	release := HoldPrivateTree(filepath.Join(root, "private"))
	defer release()
	if !filter.Contains("private") {
		t.Fatal("filter did not observe registration made after construction")
	}
}

func TestPrivateDirectoryFilterDefersMissingDirectoryResolution(t *testing.T) {
	root := t.TempDir()
	missing := filepath.Join(root, "missing")
	filter := NewPrivateDirectoryFilter(missing)
	if filter.Contains("private") {
		t.Fatal("missing directory reported private without a registration")
	}
	release := HoldPrivateTree(filepath.Join(missing, "private"))
	defer release()
	if !filter.Contains("private") {
		t.Fatal("filter did not fail closed for a subsequently registered private child")
	}
}

func BenchmarkPrivateDirectoryFilterNoPrivateTrees(b *testing.B) {
	filter := NewPrivateDirectoryFilter(filepath.Join(b.TempDir(), "missing"))
	b.ResetTimer()
	for range b.N {
		if filter.Contains("entry") {
			b.Fatal("unregistered path reported private")
		}
	}
}

func TestControlDirWatcherExclusion_AncestorAndDirectAttachments(t *testing.T) {
	home := t.TempDir()
	cfgDir := filepath.Join(home, ".paintedwolf")
	testutil.FailErr(t, "mkdir packs", os.MkdirAll(filepath.Join(cfgDir, "packs"), 0o755))
	testutil.FailErr(t, "mkdir cache", os.MkdirAll(filepath.Join(cfgDir, "cache"), 0o755))
	wtDir := filepath.Join(cfgDir, "worktrees", "wt-1")
	testutil.FailErr(t, "mkdir worktree", os.MkdirAll(wtDir, 0o755))
	t.Setenv(configdir.EnvVar, cfgDir)

	var got []string
	unbind := RegisterObserver(func(_ context.Context, event Event) {
		got = append(got, event.Paths...)
	})
	defer unbind()

	// 1. Strict ancestor ($HOME) watched: all of configdir.Root() and placements are suppressed.
	Notify(t.Context(), Event{
		ProjectDir: home,
		Kind:       WorktreeChanged,
		Source:     SourceWatcher,
		Paths: []string{
			".paintedwolf",
			".paintedwolf/paintedwolf.db",
			".paintedwolf/paintedwolf.db-wal",
			".paintedwolf/paintedwolf.db-shm",
			".paintedwolf/store.db-wal",
			".paintedwolf/settings.yaml",
			".paintedwolf-placement-1/worker-branches/p1/j1/main.go",
			"src/main.go",
		},
	})
	if !slices.Equal(got, []string{"src/main.go"}) {
		t.Fatalf("strict ancestor watcher paths = %v, want [src/main.go]", got)
	}

	// 2. Direct configdir.Root() watched: runtime DB/WAL/SHM/cache/session state suppressed, human config admitted.
	got = nil
	Notify(t.Context(), Event{
		ProjectDir: cfgDir,
		Kind:       WorktreeChanged,
		Source:     SourceWatcher,
		Paths: []string{
			"paintedwolf.db",
			"paintedwolf.db-wal",
			"paintedwolf.db-shm",
			"source-catalog.db",
			"source-catalog.db-wal",
			"store.db-wal",
			"runtime.json",
			"daemon.lock",
			"cache/index.bin",
			"sessions/s1.json",
			"settings.yaml",
			"packs/custom.yaml",
		},
	})
	if !slices.Equal(got, []string{"settings.yaml", "packs/custom.yaml"}) {
		t.Fatalf("direct control dir watcher paths = %v, want [settings.yaml packs/custom.yaml]", got)
	}

	// 3. Explicit managed workspace (<configdir>/worktrees/wt-1) watched directly: workspace files admitted.
	got = nil
	Notify(t.Context(), Event{
		ProjectDir: wtDir,
		Kind:       WorktreeChanged,
		Source:     SourceWatcher,
		Paths:      []string{"pkg/service.go"},
	})
	if !slices.Equal(got, []string{"pkg/service.go"}) {
		t.Fatalf("managed worktree watcher paths = %v, want [pkg/service.go]", got)
	}
}
