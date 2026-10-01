package repochange

import (
	"context"
	"os"
	"path/filepath"
	"testing"
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
