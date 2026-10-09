package sourceledger

import (
	"fmt"
	"testing"

	"github.com/lycaon/lycaon/internal/sourcebranch"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestDirectoryTransitionsDoNotWriteDescendants(t *testing.T) {
	testDirectoryTransitionWork(t, []int{1, 32})
}

func testDirectoryTransitionWork(t *testing.T, sizes []int) {
	t.Helper()
	var reference []int64
	for _, size := range sizes {
		t.Run(fmt.Sprint(size), func(t *testing.T) {
			store, ctx := openLedger(t)
			inputs := make([]RecordInput, size)
			for i := range inputs {
				inputs[i] = RecordInput{RecordLocation: RecordLocation{RootID: "r1", Path: fmt.Sprintf("tree/nested/%d", i)}, ProjectID: "p1", Origin: api.SourceChangeOriginUser, Op: api.SourceChangeOpCreate}
			}
			testutil.FailErr(t, "seed tracked descendants", store.RecordBatch(ctx, inputs))
			original, err := store.History.ResolveHead(ctx, "p1", sourcebranch.Trunk, "r1", "tree/nested/0")
			testutil.FailErr(t, "resolve original identity", err)
			changes := func() int64 {
				var n int64
				testutil.FailErr(t, "read write count", store.sqlDB.QueryRowContext(ctx, `SELECT generation FROM history_storage_clock WHERE id = 1`).Scan(&n))
				return n
			}
			var work []int64
			transitions := []RecordInput{
				{RecordLocation: RecordLocation{RootID: "r1", Path: "moved", FromPath: "tree", EntryKind: EntryKindDirectory}, ProjectID: "p1", Origin: api.SourceChangeOriginUser, Op: api.SourceChangeOpRename},
				{RecordLocation: RecordLocation{RootID: "r1", Path: "moved", EntryKind: EntryKindDirectory, NativeRecovery: &NativeRecoveryBinding{Key: "retained-entry"}}, ProjectID: "p1", Origin: api.SourceChangeOriginUser, Op: api.SourceChangeOpDelete, OperationID: "retained-entry"},
				{RecordLocation: RecordLocation{RootID: "r1", Path: "moved", EntryKind: EntryKindDirectory, NativeRecovery: &NativeRecoveryBinding{Key: "retained-entry", Restore: true}}, ProjectID: "p1", Origin: api.SourceChangeOriginUser, Op: api.SourceChangeOpCreate},
			}
			for _, in := range transitions {
				before := changes()
				testutil.FailErr(t, "record directory transition", store.Record(ctx, in))
				work = append(work, changes()-before)
				head, err := store.History.ResolveHeadByFile(ctx, "p1", sourcebranch.Trunk, original.FileID)
				testutil.FailErr(t, "resolve descendant by identity", err)
				if head.Path != "moved/nested/0" || head.VersionID != original.VersionID {
					t.Fatalf("descendant identity rewritten: %+v", head)
				}
				if (head.State == "absent") != (in.Op == api.SourceChangeOpDelete) {
					t.Fatalf("descendant state = %s after %s", head.State, in.Op)
				}
			}
			restored, err := store.History.ResolveHead(ctx, "p1", sourcebranch.Trunk, "r1", "moved/nested/0")
			testutil.FailErr(t, "resolve restored descendant by path", err)
			if restored.FileID != original.FileID {
				t.Fatal("native restore forked descendant identity")
			}
			if reference == nil {
				reference = work
			} else {
				for i, n := range work {
					if n != reference[i] {
						t.Fatalf("%d descendants required %d writes for transition %d; one descendant required %d", size, n, i, reference[i])
					}
				}
			}
		})
	}
}

func TestDirectoryRecreationDoesNotResurrectOldChildren(t *testing.T) {
	store, ctx := openLedger(t)
	mustRecord(t, store, ctx, RecordInput{RecordLocation: RecordLocation{RootID: "r1", Path: "tree/old"}, ProjectID: "p1", Origin: api.SourceChangeOriginUser, Op: api.SourceChangeOpCreate})
	original, err := store.History.ResolveHead(ctx, "p1", sourcebranch.Trunk, "r1", "tree/old")
	testutil.FailErr(t, "resolve old child", err)
	for _, op := range []api.SourceChangeOp{api.SourceChangeOpDelete, api.SourceChangeOpCreate} {
		mustRecord(t, store, ctx, RecordInput{RecordLocation: RecordLocation{RootID: "r1", Path: "tree", EntryKind: EntryKindDirectory}, ProjectID: "p1", Origin: api.SourceChangeOriginUser, Op: op})
	}
	head, err := store.History.ResolveHeadByFile(ctx, "p1", sourcebranch.Trunk, original.FileID)
	testutil.FailErr(t, "resolve tombstoned child", err)
	if head.State != "absent" {
		t.Fatalf("recreated directory resurrected %+v", head)
	}
	if _, err := store.History.ResolveHead(ctx, "p1", sourcebranch.Trunk, "r1", "tree/old"); err != ErrHistoryNotFound {
		t.Fatalf("old child remains visible: %v", err)
	}
}

func TestDirectoryTransitionsPreserveCurrentComparisonAndDeletedContent(t *testing.T) {
	store, ctx := openLedger(t)
	mustRecord(t, store, ctx, RecordInput{RecordLocation: RecordLocation{RootID: "r1", Path: "tree/child"}, ProjectID: "p1", Origin: api.SourceChangeOriginUser, Op: api.SourceChangeOpCreate, After: []byte("kept")})
	original, err := store.History.ResolveHead(ctx, "p1", sourcebranch.Trunk, "r1", "tree/child")
	testutil.FailErr(t, "resolve child", err)
	mustRecord(t, store, ctx, RecordInput{RecordLocation: RecordLocation{RootID: "r1", Path: "moved", FromPath: "tree", EntryKind: EntryKindDirectory}, ProjectID: "p1", Origin: api.SourceChangeOriginUser, Op: api.SourceChangeOpRename})
	comparison, err := store.Comparisons.CompareScope(ctx, "p1", sourcebranch.Trunk, Baseline{}, original.FileID, ScopeComparisonOptions{})
	testutil.FailErr(t, "compare moved child", err)
	if comparison.After.Path != "moved/child" || comparison.After.Content != "kept" {
		t.Fatalf("comparison lost current namespace: %+v", comparison)
	}
	mustRecord(t, store, ctx, RecordInput{RecordLocation: RecordLocation{RootID: "r1", Path: "moved", EntryKind: EntryKindDirectory}, ProjectID: "p1", Origin: api.SourceChangeOriginUser, Op: api.SourceChangeOpDelete})
	deleted, err := store.History.ResolveDeletedPath(ctx, "p1", sourcebranch.Trunk, "r1", "moved/child")
	testutil.FailErr(t, "resolve child deleted with parent", err)
	retained, err := store.History.DeletedPathContent(ctx, "p1", deleted)
	testutil.FailErr(t, "read content removed with parent", err)
	if retained.Content != "kept" {
		t.Fatalf("deleted child content = %+v", retained)
	}
	comparison, err = store.Comparisons.CompareScope(ctx, "p1", sourcebranch.Trunk, Baseline{}, original.FileID, ScopeComparisonOptions{})
	testutil.FailErr(t, "compare child removed with parent", err)
	if comparison.After.Availability != ContentAbsent || comparison.After.Content != "" {
		t.Fatalf("comparison resurrected child: %+v", comparison)
	}
}

func TestEditAfterDirectoryMoveRetainsCurrentBeforeLocation(t *testing.T) {
	store, ctx := openLedger(t)
	mustRecord(t, store, ctx, RecordInput{RecordLocation: RecordLocation{RootID: "r1", Path: "tree/child"}, ProjectID: "p1", Origin: api.SourceChangeOriginUser, Op: api.SourceChangeOpCreate, After: []byte("old")})
	mustRecord(t, store, ctx, RecordInput{RecordLocation: RecordLocation{RootID: "r1", Path: "moved", FromPath: "tree", EntryKind: EntryKindDirectory}, ProjectID: "p1", Origin: api.SourceChangeOriginUser, Op: api.SourceChangeOpRename})
	mustRecord(t, store, ctx, RecordInput{RecordLocation: RecordLocation{RootID: "r1", Path: "moved/child"}, ProjectID: "p1", Origin: api.SourceChangeOriginAgent, Op: api.SourceChangeOpWrite, Before: []byte("old"), After: []byte("new")})
	head, err := store.History.ResolveHead(ctx, "p1", sourcebranch.Trunk, "r1", "moved/child")
	testutil.FailErr(t, "resolve edited child", err)
	comparison, err := store.Comparisons.CompareVersions(ctx, "p1", head.VersionID)
	testutil.FailErr(t, "compare edit after parent move", err)
	if comparison.LocationChanged || comparison.Before.Path != "moved/child" || comparison.Before.Content != "old" || comparison.After.Content != "new" {
		t.Fatalf("edit inherited stale location: %+v", comparison)
	}
}

func TestPublicationRetiresStaleDestination(t *testing.T) {
	for _, directory := range []bool{false, true} {
		t.Run(fmt.Sprint(directory), func(t *testing.T) {
			store, ctx := openLedger(t)
			record := func(rel, kind string, op api.SourceChangeOp, from string) {
				mustRecord(t, store, ctx, RecordInput{RecordLocation: RecordLocation{RootID: "r1", Path: rel, EntryKind: kind, FromPath: from}, ProjectID: "p1", Origin: api.SourceChangeOriginUser, Op: op})
			}
			record("target", EntryKindDirectory, api.SourceChangeOpCreate, "")
			record("target/old", EntryKindFile, api.SourceChangeOpCreate, "")
			oldRoot, err := store.History.ResolveHead(ctx, "p1", sourcebranch.Trunk, "r1", "target")
			testutil.FailErr(t, "resolve old root", err)
			oldChild, err := store.History.ResolveHead(ctx, "p1", sourcebranch.Trunk, "r1", "target/old")
			testutil.FailErr(t, "resolve old child", err)
			if directory {
				record("source", EntryKindDirectory, api.SourceChangeOpCreate, "")
				record("source/kept", EntryKindFile, api.SourceChangeOpCreate, "")
				source, err := store.History.ResolveHead(ctx, "p1", sourcebranch.Trunk, "r1", "source/kept")
				testutil.FailErr(t, "resolve source child", err)
				record("target", EntryKindDirectory, api.SourceChangeOpRename, "source")
				moved, err := store.History.ResolveHead(ctx, "p1", sourcebranch.Trunk, "r1", "target/kept")
				testutil.FailErr(t, "resolve moved child", err)
				if moved.FileID != source.FileID || moved.VersionID != source.VersionID {
					t.Fatal("move rewrote descendant identity")
				}
			} else {
				record("target", EntryKindFile, api.SourceChangeOpCreate, "")
			}
			for _, id := range []string{oldRoot.FileID, oldChild.FileID} {
				head, err := store.History.ResolveHeadByFile(ctx, "p1", sourcebranch.Trunk, id)
				testutil.FailErr(t, "resolve stale identity", err)
				if head.State != "absent" {
					t.Fatalf("stale occupant remains visible: %+v", head)
				}
			}
			current, err := store.History.ResolveHead(ctx, "p1", sourcebranch.Trunk, "r1", "target")
			testutil.FailErr(t, "resolve publication", err)
			if current.FileID == oldRoot.FileID {
				t.Fatal("publication reused stale identity")
			}
		})
	}
}
