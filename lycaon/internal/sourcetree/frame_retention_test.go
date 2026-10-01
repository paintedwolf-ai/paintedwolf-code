package sourcetree

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/backgroundwork"
	"github.com/lycaon/lycaon/internal/sourcecatalog"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestFrameRetentionRejectsChangesBeforeCachedRows(t *testing.T) {
	view, root := viewFixture(t)
	for _, dir := range []string{"a", "z"} {
		testutil.FailErr(t, "create directory", os.Mkdir(filepath.Join(root.Path, dir), 0700))
		testutil.FailErr(t, "create file", os.WriteFile(filepath.Join(root.Path, dir, "file.txt"), []byte("source"), 0600))
	}
	observeFixture(t, view, root)
	address := Address{Root: root.ID, Path: "."}
	request := FrameRequest{Anchor: &address, Limit: 3}
	first, err := frameForTest(t, view, t.Context(), request)
	testutil.FailErr(t, "read retained frame", err)
	if first.Prefix == nil {
		t.Fatal("tree frame did not publish a prefix proof")
	}
	request.Retain = []FramePrefix{*first.Prefix}
	for _, dir := range []string{"z", "a"} {
		rel := filepath.Join(dir, "added.txt")
		testutil.FailErr(t, "insert directory child", os.WriteFile(filepath.Join(root.Path, rel), []byte("added"), 0600))
		view.catalog.InvalidateRoot(root.Path, filepath.ToSlash(rel))
		_, err := view.catalog.ObserveDirectory(t.Context(), view.scope.Project, root, dir, sourcecatalog.DirectoryRead{Priority: backgroundwork.PriorityInteractive})
		testutil.FailErr(t, "publish changed child", err)
		frame, err := frameForTest(t, view, t.Context(), request)
		testutil.FailErr(t, "validate retained prefix", err)
		if dir == "z" && (frame.RetainedPrefix == nil || *frame.RetainedPrefix != *first.Prefix) {
			t.Fatalf("unrelated suffix invalidated prefix: %+v", frame.RetainedPrefix)
		}
		if dir == "a" && frame.RetainedPrefix != nil {
			t.Fatalf("inserted prefix preserved stale row positions: %+v", frame.RetainedPrefix)
		}
	}
}

func TestFrameRetentionSeparatesDisclosureIntent(t *testing.T) {
	view, root := viewFixture(t)
	testutil.FailErr(t, "create file", os.WriteFile(filepath.Join(root.Path, "file.txt"), []byte("source"), 0600))
	<-view.Prepare()
	address := Address{Root: root.ID, Path: "."}
	request := FrameRequest{Anchor: &address, Limit: 1}
	first, err := frameForTest(t, view, t.Context(), request)
	testutil.FailErr(t, "read first root", err)
	request.Retain = []FramePrefix{*first.Prefix}
	testutil.FailErr(t, "change disclosure", view.Disclose(t.Context(), nil, IntentEntry{Address: address, Disclosure: Disclosure{Open: false}}))
	frame, err := frameForTest(t, view, t.Context(), request)
	testutil.FailErr(t, "read changed intent", err)
	if frame.RetainedPrefix != nil {
		t.Fatal("retained rows across disclosure intents")
	}
}

func TestFrameRetentionPreservesClosedExceptionsAndSelectsLargestProof(t *testing.T) {
	view, root := viewFixture(t)
	for _, dir := range []string{"a/nested", "z"} {
		testutil.FailErr(t, "create directory", os.MkdirAll(filepath.Join(root.Path, dir), 0700))
		testutil.FailErr(t, "create file", os.WriteFile(filepath.Join(root.Path, dir, "file.txt"), []byte("source"), 0600))
	}
	observeFixture(t, view, root)
	testutil.FailErr(t, "collapse exception", view.Disclose(t.Context(), nil, IntentEntry{Address: Address{Root: root.ID, Path: "a"}, Disclosure: Disclosure{Open: false}}))
	address := Address{Root: root.ID, Path: "."}
	request := FrameRequest{Anchor: &address, Limit: 200}
	first, err := frameForTest(t, view, t.Context(), request)
	testutil.FailErr(t, "read complete frame", err)
	request.Limit = 1
	short, err := frameForTest(t, view, t.Context(), request)
	testutil.FailErr(t, "read root prefix", err)
	request.Retain = []FramePrefix{*short.Prefix, *first.Prefix}
	testutil.FailErr(t, "change closed descendant", os.WriteFile(filepath.Join(root.Path, "a", "nested", "added.txt"), []byte("added"), 0600))
	view.catalog.InvalidateRoot(root.Path, "a/nested/added.txt")
	_, err = view.catalog.ObserveDirectory(t.Context(), view.scope.Project, root, "a/nested", sourcecatalog.DirectoryRead{Priority: backgroundwork.PriorityInteractive})
	testutil.FailErr(t, "observe closed descendant", err)
	frame, err := frameForTest(t, view, t.Context(), request)
	testutil.FailErr(t, "retain unchanged exception", err)
	if frame.RetainedPrefix == nil || *frame.RetainedPrefix != *first.Prefix {
		t.Fatalf("retained=%+v want=%+v", frame.RetainedPrefix, first.Prefix)
	}
	testutil.FailErr(t, "insert preceding directory", os.Mkdir(filepath.Join(root.Path, "0"), 0700))
	view.catalog.InvalidateRoot(root.Path, "0")
	_, err = view.catalog.ObserveDirectory(t.Context(), view.scope.Project, root, ".", sourcecatalog.DirectoryRead{Priority: backgroundwork.PriorityInteractive})
	testutil.FailErr(t, "observe inserted directory", err)
	covered := func(ctx context.Context, navigation *sourcecatalog.Navigation) (bool, error) {
		return sourcecatalog.SubtreeCovered(ctx, navigation, ".")
	}
	testutil.FailErr(t, "complete inserted directory before comparing updated coordinates",
		view.catalog.AwaitSubtree(t.Context(), view.scope.Project, root, ".", covered))
	frame, err = frameForTest(t, view, t.Context(), request)
	testutil.FailErr(t, "retain smaller valid prefix", err)
	if frame.RetainedPrefix == nil || *frame.RetainedPrefix != *short.Prefix {
		t.Fatalf("retained=%+v want=%+v", frame.RetainedPrefix, short.Prefix)
	}
}
