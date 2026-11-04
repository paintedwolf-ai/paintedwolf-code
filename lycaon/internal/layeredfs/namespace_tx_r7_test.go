package layeredfs

import (
	"errors"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
)

func TestConcurrentRenameSameSourceForcedInterleaving(t *testing.T) {
	rootDir, backingDir := t.TempDir(), t.TempDir()
	testutil.FailErr(t, "write lower a.txt", os.WriteFile(filepath.Join(rootDir, "a.txt"), []byte("payload\n"), 0o644))

	entered := make(chan struct{}, 2)
	releaseGate := make(chan struct{})
	var closeGateOnce sync.Once
	openGate := func() { closeGateOnce.Do(func() { close(releaseGate) }) }

	var hookCalls atomic.Int32
	c, err := New(Config{
		ViewID:     "view-r7-rename-same-src",
		BackingDir: backingDir,
		Roots:      []RootSpec{{ID: "r0", Label: "r0", TrunkPath: rootDir, IsPrimary: true}},
		FaultHooks: &FaultHooks{
			NamespacePreCommitHook: func(op string, qpath QualifiedPath) {
				if op == "rename" && qpath.Rel == "a.txt" && hookCalls.Add(1) <= 2 {
					entered <- struct{}{}
					<-releaseGate
				}
			},
		},
	})
	testutil.FailErr(t, "new core", err)
	t.Cleanup(func() { openGate(); _ = c.ReleaseView() })

	qpA := QualifiedPath{RootID: "r0", Rel: "a.txt"}
	qpB := QualifiedPath{RootID: "r0", Rel: "b.txt"}
	qpC := QualifiedPath{RootID: "r0", Rel: "c.txt"}

	errCh := make(chan error, 2)
	go func() { errCh <- c.Rename(qpA, qpB) }()
	go func() { errCh <- c.Rename(qpA, qpC) }()
	<-entered
	<-entered
	openGate()

	verifyRenameSameSourceOutcome(t, c, qpA, qpB, qpC, <-errCh, <-errCh)
}

func verifyRenameSameSourceOutcome(t *testing.T, c *Core, qpA, qpB, qpC QualifiedPath, err1, err2 error) {
	t.Helper()
	if (err1 == nil) == (err2 == nil) {
		t.Fatalf("expected exactly one Rename to succeed, got err1=%v err2=%v", err1, err2)
	}
	loserErr := err1
	if err2 != nil {
		loserErr = err2
	}
	if !errors.Is(loserErr, os.ErrNotExist) {
		t.Fatalf("expected losing Rename to fail with ErrNotExist, got %v", loserErr)
	}
	if _, err := c.Stat(qpA); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("expected source a.txt absent after rename, got %v", err)
	}
	stB, errB := c.Stat(qpB)
	stC, errC := c.Stat(qpC)
	if (errB == nil) == (errC == nil) {
		t.Fatalf("expected exactly one destination to exist, got errB=%v errC=%v", errB, errC)
	}
	winnerSt := stB
	if errC == nil {
		winnerSt = stC
	}
	c.mu.RLock()
	pathCount := len(c.state.nodesByID[winnerSt.NodeID].paths)
	c.mu.RUnlock()
	if winnerSt.Nlink != 1 || pathCount != 1 {
		t.Fatalf("expected winning node Nlink=1 and paths=1, got Nlink=%d paths=%d", winnerSt.Nlink, pathCount)
	}
}

func TestConcurrentLinkSameDestinationForcedInterleaving(t *testing.T) {
	rootDir, backingDir := t.TempDir(), t.TempDir()
	testutil.FailErr(t, "write lower src1.txt", os.WriteFile(filepath.Join(rootDir, "src1.txt"), []byte("one\n"), 0o644))
	testutil.FailErr(t, "write lower src2.txt", os.WriteFile(filepath.Join(rootDir, "src2.txt"), []byte("two\n"), 0o644))

	entered := make(chan struct{}, 2)
	releaseGate := make(chan struct{})
	var closeGateOnce sync.Once
	openGate := func() { closeGateOnce.Do(func() { close(releaseGate) }) }

	var hookCalls atomic.Int32
	c, err := New(Config{
		ViewID:     "view-r7-link-same-dst",
		BackingDir: backingDir,
		Roots:      []RootSpec{{ID: "r0", Label: "r0", TrunkPath: rootDir, IsPrimary: true}},
		FaultHooks: &FaultHooks{
			NamespacePreCommitHook: func(op string, qpath QualifiedPath) {
				if op == "link" && qpath.Rel == "dst.txt" && hookCalls.Add(1) <= 2 {
					entered <- struct{}{}
					<-releaseGate
				}
			},
		},
	})
	testutil.FailErr(t, "new core", err)
	t.Cleanup(func() { openGate(); _ = c.ReleaseView() })

	qpSrc1 := QualifiedPath{RootID: "r0", Rel: "src1.txt"}
	qpSrc2 := QualifiedPath{RootID: "r0", Rel: "src2.txt"}
	qpDst := QualifiedPath{RootID: "r0", Rel: "dst.txt"}

	errCh := make(chan error, 2)
	go func() { _, lErr := c.Link(qpSrc1, qpDst); errCh <- lErr }()
	go func() { _, lErr := c.Link(qpSrc2, qpDst); errCh <- lErr }()
	<-entered
	<-entered
	openGate()

	verifyLinkSameDestinationOutcome(t, c, qpSrc1, qpSrc2, qpDst, <-errCh, <-errCh)
}

func verifyLinkSameDestinationOutcome(t *testing.T, c *Core, qpSrc1, qpSrc2, qpDst QualifiedPath, err1, err2 error) {
	t.Helper()
	if (err1 == nil) == (err2 == nil) {
		t.Fatalf("expected exactly one Link to succeed, got err1=%v err2=%v", err1, err2)
	}
	loserErr := err1
	if err2 != nil {
		loserErr = err2
	}
	if !errors.Is(loserErr, os.ErrExist) {
		t.Fatalf("expected losing Link to fail with ErrExist, got %v", loserErr)
	}
	st1, err := c.Stat(qpSrc1)
	testutil.FailErr(t, "Stat src1.txt", err)
	st2, err := c.Stat(qpSrc2)
	testutil.FailErr(t, "Stat src2.txt", err)
	stDst, err := c.Stat(qpDst)
	testutil.FailErr(t, "Stat dst.txt", err)

	if stDst.Nlink == 2 && ((st1.Nlink == 2 && st2.Nlink == 1 && stDst.NodeID == st1.NodeID) ||
		(st2.Nlink == 2 && st1.Nlink == 1 && stDst.NodeID == st2.NodeID)) {
		return
	}
	t.Fatalf("unexpected link counts/node IDs: src1=(id=%d,nlink=%d) src2=(id=%d,nlink=%d) dst=(id=%d,nlink=%d)",
		st1.NodeID, st1.Nlink, st2.NodeID, st2.Nlink, stDst.NodeID, stDst.Nlink)
}

func TestConcurrentUnlinkDirAndChildCreateForcedInterleavings(t *testing.T) {
	t.Run("child_created_before_unlink_commits", testUnlinkDirBlockedByConcurrentChildCreate)
	t.Run("dir_unlinked_before_child_create_commits", testChildCreateBlockedByConcurrentUnlinkDir)
}

func testUnlinkDirBlockedByConcurrentChildCreate(t *testing.T) {
	rootDir, backingDir := t.TempDir(), t.TempDir()
	testutil.FailErr(t, "mkdir lower dir", os.MkdirAll(filepath.Join(rootDir, "dir"), 0o755))

	unlinkReady, releaseUnlink := make(chan struct{}), make(chan struct{})
	var readyOnce, releaseOnce sync.Once
	openUnlink := func() { releaseOnce.Do(func() { close(releaseUnlink) }) }

	c, err := New(Config{
		ViewID:     "view-r7-unlink-vs-create",
		BackingDir: backingDir,
		Roots:      []RootSpec{{ID: "r0", Label: "r0", TrunkPath: rootDir, IsPrimary: true}},
		FaultHooks: &FaultHooks{
			NamespacePreCommitHook: func(op string, qpath QualifiedPath) {
				if op == "unlink" && qpath.Rel == "dir" {
					readyOnce.Do(func() { close(unlinkReady); <-releaseUnlink })
				}
			},
		},
	})
	testutil.FailErr(t, "new core", err)
	t.Cleanup(func() { openUnlink(); _ = c.ReleaseView() })

	qpDir := QualifiedPath{RootID: "r0", Rel: "dir"}
	qpChild := QualifiedPath{RootID: "r0", Rel: "dir/child.txt"}
	unlinkDone := make(chan error, 1)
	go func() { unlinkDone <- c.Unlink(qpDir, true) }()

	<-unlinkReady
	_, err = c.WriteFile(qpChild, []byte("created\n"), 0o644)
	testutil.FailErr(t, "WriteFile dir/child.txt", err)
	openUnlink()

	if uErr := <-unlinkDone; !errors.Is(uErr, syscall.ENOTEMPTY) {
		t.Fatalf("expected Unlink(dir, true) to fail with ENOTEMPTY, got %v", uErr)
	}
	data, err := c.ReadFile(qpChild)
	testutil.FailErr(t, "ReadFile dir/child.txt", err)
	if string(data) != "created\n" {
		t.Fatalf("unexpected child content %q", string(data))
	}
}

func testChildCreateBlockedByConcurrentUnlinkDir(t *testing.T) {
	rootDir, backingDir := t.TempDir(), t.TempDir()
	testutil.FailErr(t, "mkdir lower dir2", os.MkdirAll(filepath.Join(rootDir, "dir2"), 0o755))

	createReady, releaseCreate := make(chan struct{}), make(chan struct{})
	var readyOnce, releaseOnce sync.Once
	openCreate := func() { releaseOnce.Do(func() { close(releaseCreate) }) }

	c, err := New(Config{
		ViewID:     "view-r7-create-vs-unlink",
		BackingDir: backingDir,
		Roots:      []RootSpec{{ID: "r0", Label: "r0", TrunkPath: rootDir, IsPrimary: true}},
		FaultHooks: &FaultHooks{
			NamespacePreCommitHook: func(op string, qpath QualifiedPath) {
				if op == "open" && qpath.Rel == "dir2/child.txt" {
					readyOnce.Do(func() { close(createReady); <-releaseCreate })
				}
			},
		},
	})
	testutil.FailErr(t, "new core", err)
	t.Cleanup(func() { openCreate(); _ = c.ReleaseView() })

	qpDir := QualifiedPath{RootID: "r0", Rel: "dir2"}
	qpChild := QualifiedPath{RootID: "r0", Rel: "dir2/child.txt"}
	createDone := make(chan error, 1)
	go func() { _, cErr := c.WriteFile(qpChild, []byte("orphan\n"), 0o644); createDone <- cErr }()

	<-createReady
	testutil.FailErr(t, "Unlink dir2", c.Unlink(qpDir, true))
	openCreate()

	if cErr := <-createDone; !errors.Is(cErr, os.ErrNotExist) {
		t.Fatalf("expected WriteFile(dir2/child.txt) to fail with ErrNotExist, got %v", cErr)
	}
	if _, err := c.Stat(qpChild); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("expected dir2/child.txt to remain absent, got %v", err)
	}
}

func TestConcurrentDirectoryRenameForcedInterleavings(t *testing.T) {
	t.Run("destination_becomes_non_empty_before_commit", testDirRenameDestinationBecomesNonEmpty)
	t.Run("new_child_added_to_source_subtree_retries_and_unlinks_replaced_dst", testDirRenameSubtreeChildAddedAndReplacedDstUnlinked)
}

func testDirRenameDestinationBecomesNonEmpty(t *testing.T) {
	rootDir, backingDir := t.TempDir(), t.TempDir()
	testutil.FailErr(t, "mkdir srcDir", os.MkdirAll(filepath.Join(rootDir, "srcDir"), 0o755))
	testutil.FailErr(t, "write srcDir/item.txt", os.WriteFile(filepath.Join(rootDir, "srcDir", "item.txt"), []byte("item\n"), 0o644))
	testutil.FailErr(t, "mkdir dstDir", os.MkdirAll(filepath.Join(rootDir, "dstDir"), 0o755))

	renameReady, releaseRename := make(chan struct{}), make(chan struct{})
	var readyOnce, releaseOnce sync.Once
	openRename := func() { releaseOnce.Do(func() { close(releaseRename) }) }

	c, err := New(Config{
		ViewID:     "view-r7-dir-rename-nonempty",
		BackingDir: backingDir,
		Roots:      []RootSpec{{ID: "r0", Label: "r0", TrunkPath: rootDir, IsPrimary: true}},
		FaultHooks: &FaultHooks{
			NamespacePreCommitHook: func(op string, qpath QualifiedPath) {
				if op == "rename" && qpath.Rel == "srcDir" {
					readyOnce.Do(func() { close(renameReady); <-releaseRename })
				}
			},
		},
	})
	testutil.FailErr(t, "new core", err)
	t.Cleanup(func() { openRename(); _ = c.ReleaseView() })

	qpSrc, qpDst := QualifiedPath{RootID: "r0", Rel: "srcDir"}, QualifiedPath{RootID: "r0", Rel: "dstDir"}
	renameDone := make(chan error, 1)
	go func() { renameDone <- c.Rename(qpSrc, qpDst) }()

	<-renameReady
	_, err = c.WriteFile(QualifiedPath{RootID: "r0", Rel: "dstDir/blocker.txt"}, []byte("block\n"), 0o644)
	testutil.FailErr(t, "WriteFile dstDir/blocker.txt", err)
	openRename()

	if rErr := <-renameDone; !errors.Is(rErr, syscall.ENOTEMPTY) {
		t.Fatalf("expected Rename(srcDir, dstDir) to fail with ENOTEMPTY, got %v", rErr)
	}
	if _, err := c.Stat(QualifiedPath{RootID: "r0", Rel: "srcDir/item.txt"}); err != nil {
		t.Fatalf("expected srcDir/item.txt to remain intact, got %v", err)
	}
}

func testDirRenameSubtreeChildAddedAndReplacedDstUnlinked(t *testing.T) {
	rootDir, backingDir := t.TempDir(), t.TempDir()
	testutil.FailErr(t, "mkdir srcDir2", os.MkdirAll(filepath.Join(rootDir, "srcDir2"), 0o755))
	testutil.FailErr(t, "write srcDir2/early.txt", os.WriteFile(filepath.Join(rootDir, "srcDir2", "early.txt"), []byte("early\n"), 0o644))

	renameReady, releaseRename := make(chan struct{}), make(chan struct{})
	var readyOnce, releaseOnce sync.Once
	openRename := func() { releaseOnce.Do(func() { close(releaseRename) }) }

	c, err := New(Config{
		ViewID:     "view-r7-dir-rename-retry",
		BackingDir: backingDir,
		Roots:      []RootSpec{{ID: "r0", Label: "r0", TrunkPath: rootDir, IsPrimary: true}},
		FaultHooks: &FaultHooks{
			NamespacePreCommitHook: func(op string, qpath QualifiedPath) {
				if op == "rename" && qpath.Rel == "srcDir2" {
					readyOnce.Do(func() { close(renameReady); <-releaseRename })
				}
			},
		},
	})
	testutil.FailErr(t, "new core", err)
	t.Cleanup(func() { openRename(); _ = c.ReleaseView() })

	qpSrc, qpDst := QualifiedPath{RootID: "r0", Rel: "srcDir2"}, QualifiedPath{RootID: "r0", Rel: "dstDir2"}
	dstStBefore, err := c.Mkdir(qpDst, 0o755)
	testutil.FailErr(t, "Mkdir dstDir2", err)

	renameDone := make(chan error, 1)
	go func() { renameDone <- c.Rename(qpSrc, qpDst) }()

	<-renameReady
	_, err = c.WriteFile(QualifiedPath{RootID: "r0", Rel: "srcDir2/late.txt"}, []byte("late\n"), 0o644)
	testutil.FailErr(t, "WriteFile srcDir2/late.txt", err)
	openRename()

	testutil.FailErr(t, "Rename srcDir2 -> dstDir2", <-renameDone)
	for _, rel := range []string{"dstDir2/early.txt", "dstDir2/late.txt"} {
		if _, err := c.Stat(QualifiedPath{RootID: "r0", Rel: rel}); err != nil {
			t.Fatalf("expected %s after rename retry, got %v", rel, err)
		}
	}
	c.mu.RLock()
	replacedNode := c.state.nodesByID[dstStBefore.NodeID]
	unlinked := replacedNode.flags.unlinked && len(replacedNode.paths) == 0
	c.mu.RUnlock()
	if !unlinked {
		t.Fatalf("expected replaced dstDir2 node %d to be marked unlinked with 0 paths", dstStBefore.NodeID)
	}
}
