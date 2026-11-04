package layeredfs

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/testutil"
)

func newSingleRootTestCore(t *testing.T) (*Core, string, string) {
	t.Helper()
	tmp := t.TempDir()
	trunk := filepath.Join(tmp, "trunk")
	backing := filepath.Join(tmp, "backing")
	testutil.FailErr(t, "mkdir trunk", os.MkdirAll(trunk, 0o755))
	testutil.FailErr(t, "mkdir backing", os.MkdirAll(backing, 0o700))

	core, err := New(Config{
		ViewID:     "view-test-1",
		BackingDir: backing,
		Roots: []RootSpec{
			{ID: "primary", Label: "primary", TrunkPath: trunk, IsPrimary: true},
		},
	})
	testutil.FailErr(t, "New Core", err)
	t.Cleanup(func() {
		_ = core.ReleaseView()
	})
	return core, trunk, backing
}

func TestScenarioSuiteAllOperations(t *testing.T) {
	t.Parallel()
	core, trunk, _ := newSingleRootTestCore(t)

	// Seed trunk fixtures.
	seedScenarioTrunk(t, trunk)

	qp := func(rel string) QualifiedPath {
		return QualifiedPath{RootID: "primary", Rel: rel}
	}

	// 1. Create / modify / delete
	_, err := core.WriteFile(qp("created.txt"), []byte("brand-new"), 0o644)
	testutil.FailErr(t, "create created.txt", err)
	gotCreated, err := core.ReadFile(qp("created.txt"))
	testutil.FailErr(t, "read created.txt", err)
	if string(gotCreated) != "brand-new" {
		t.Fatalf("unexpected created.txt content: %q", string(gotCreated))
	}
	baseCreated, ok := core.CapturedBase(qp("created.txt"))
	if !ok || baseCreated.Kind != EntryAbsent {
		t.Fatalf("expected absent base for new path, got %+v (ok=%v)", baseCreated, ok)
	}

	_, err = core.WriteFile(qp("base.txt"), []byte("mutated-private-content\n"), 0o644)
	testutil.FailErr(t, "modify base.txt", err)
	trunkBytes, err := os.ReadFile(filepath.Join(trunk, "base.txt"))
	testutil.FailErr(t, "read trunk base.txt", err)
	if string(trunkBytes) != "initial-trunk-content\n" {
		t.Fatalf("trunk base.txt was mutated: %q", string(trunkBytes))
	}
	baseCaptured, ok := core.CapturedBase(qp("base.txt"))
	if !ok || baseCaptured.Kind != EntryRegular || baseCaptured.Size != int64(len("initial-trunk-content\n")) {
		t.Fatalf("unexpected base capture for base.txt: %+v", baseCaptured)
	}

	// 2. Truncate
	truncAttr, err := core.Truncate(qp("trunc.txt"), 4)
	testutil.FailErr(t, "truncate trunc.txt", err)
	if truncAttr.Size != 4 {
		t.Fatalf("expected truncated size 4, got %d", truncAttr.Size)
	}
	gotTrunc, err := core.ReadFile(qp("trunc.txt"))
	testutil.FailErr(t, "read trunc.txt", err)
	if string(gotTrunc) != "0123" {
		t.Fatalf("unexpected truncated bytes: %q", string(gotTrunc))
	}

	// 3. sed -i style temp create + rename-over existing lower file
	_, err = core.WriteFile(qp(".sed.tmp"), []byte("hello layered world\n"), 0o600)
	testutil.FailErr(t, "write .sed.tmp", err)
	testutil.FailErr(t, "rename .sed.tmp over sed.txt", core.Rename(qp(".sed.tmp"), qp("sed.txt")))
	sedBase, ok := core.CapturedBase(qp("sed.txt"))
	if !ok || sedBase.Kind != EntryRegular || sedBase.Size != int64(len("hello world\n")) {
		t.Fatalf("rename-over must capture destination base even from private temp file, got %+v", sedBase)
	}
	gotSed, err := core.ReadFile(qp("sed.txt"))
	testutil.FailErr(t, "read sed.txt after rename-over", err)
	if string(gotSed) != "hello layered world\n" {
		t.Fatalf("unexpected sed.txt content: %q", string(gotSed))
	}

	// 4. File-to-directory replacement and directory-to-file replacement back
	testutil.FailErr(t, "unlink replace_me file", core.Unlink(qp("replace_me"), false))
	_, err = core.Mkdir(qp("replace_me"), 0o755)
	testutil.FailErr(t, "mkdir replace_me", err)
	_, err = core.WriteFile(qp("replace_me/inside.txt"), []byte("inside-dir"), 0o644)
	testutil.FailErr(t, "write replace_me/inside.txt", err)
	testutil.FailErr(t, "removeAll replace_me dir", core.RemoveAll(qp("replace_me")))
	_, err = core.WriteFile(qp("replace_me"), []byte("back-to-file"), 0o644)
	testutil.FailErr(t, "write replace_me back to file", err)
	gotReplace, err := core.ReadFile(qp("replace_me"))
	testutil.FailErr(t, "read replace_me", err)
	if string(gotReplace) != "back-to-file" {
		t.Fatalf("unexpected replace_me content: %q", string(gotReplace))
	}

	// 5. Directory rename
	testutil.FailErr(t, "rename dir_src to dir_dst", core.Rename(qp("dir_src"), qp("dir_dst")))
	if _, err := core.Stat(qp("dir_src")); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("expected dir_src to be absent after rename, got %v", err)
	}
	gotMoved, err := core.ReadFile(qp("dir_dst/nested/item.txt"))
	testutil.FailErr(t, "read dir_dst/nested/item.txt", err)
	if string(gotMoved) != "nested-item" {
		t.Fatalf("unexpected moved nested content: %q", string(gotMoved))
	}

	// 6. Open-unlink: open handle, unlink pathname, continue reading and writing via handle
	ouHandle, _, err := core.Open(qp("open_unlink.txt"), OpenReadWrite, 0)
	testutil.FailErr(t, "open open_unlink.txt", err)
	testutil.FailErr(t, "unlink open_unlink.txt while open", core.Unlink(qp("open_unlink.txt"), false))
	if _, err := core.Stat(qp("open_unlink.txt")); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("expected unlinked path to return ErrNotExist, got %v", err)
	}
	_, err = core.WriteHandle(ouHandle, []byte("-and-written"), int64(len("survive-unlink")))
	testutil.FailErr(t, "write to open-unlinked handle", err)
	ouBuf := make([]byte, 64)
	n, err := core.ReadHandle(ouHandle, ouBuf, 0)
	testutil.FailErr(t, "read from open-unlinked handle", err)
	if string(ouBuf[:n]) != "survive-unlink-and-written" {
		t.Fatalf("unexpected open-unlinked handle bytes: %q", string(ouBuf[:n]))
	}
	testutil.FailErr(t, "close open-unlinked handle", core.CloseHandle(ouHandle))

	// 7. Hard links: shared private node identity without scanning project
	linkAttr, err := core.Link(qp("hard_target.txt"), qp("hard_alias.txt"))
	testutil.FailErr(t, "create hard link", err)
	origAttr, err := core.Stat(qp("hard_target.txt"))
	testutil.FailErr(t, "stat hard_target.txt", err)
	if linkAttr.NodeID != origAttr.NodeID || linkAttr.Nlink != 2 {
		t.Fatalf("expected shared NodeID and Nlink=2, got link=%+v orig=%+v", linkAttr, origAttr)
	}
	_, err = core.WriteFile(qp("hard_alias.txt"), []byte("updated-via-alias"), 0o644)
	testutil.FailErr(t, "write via hard_alias.txt", err)
	gotViaOrig, err := core.ReadFile(qp("hard_target.txt"))
	testutil.FailErr(t, "read via hard_target.txt", err)
	if string(gotViaOrig) != "updated-via-alias" {
		t.Fatalf("hard link alias did not share underlying content: %q", string(gotViaOrig))
	}

	// 8. Symlinks
	_, err = core.Symlink("hard_target.txt", qp("sym_to_target"))
	testutil.FailErr(t, "create symlink", err)
	symTarget, err := core.Readlink(qp("sym_to_target"))
	testutil.FailErr(t, "readlink sym_to_target", err)
	if symTarget != "hard_target.txt" {
		t.Fatalf("unexpected readlink result: %q", symTarget)
	}
	gotViaSym, err := core.ReadFile(qp("sym_to_target"))
	testutil.FailErr(t, "read through symlink", err)
	if string(gotViaSym) != "updated-via-alias" {
		t.Fatalf("unexpected content through symlink: %q", string(gotViaSym))
	}

	// 9. Chmod & Timestamps
	fixedTime := time.Date(2026, 5, 1, 12, 0, 0, 0, time.UTC)
	_, err = core.Chmod(qp("base.txt"), 0o755)
	testutil.FailErr(t, "chmod base.txt", err)
	_, err = core.Chtimes(qp("base.txt"), fixedTime, fixedTime, false)
	testutil.FailErr(t, "chtimes base.txt", err)
	attrAfterMeta, err := core.Stat(qp("base.txt"))
	testutil.FailErr(t, "stat base.txt after chmod/chtimes", err)
	if attrAfterMeta.Mode.Perm() != 0o755 || !attrAfterMeta.ModTime.Equal(fixedTime) {
		t.Fatalf("expected mode 0755 and mtime %v, got mode=%o mtime=%v", fixedTime, attrAfterMeta.Mode.Perm(), attrAfterMeta.ModTime)
	}

	scenarioXattrsAndAppleDouble(t, core, qp)
	scenarioCopyUpWithoutChangeIsNotLogical(t, core, qp)
	scenarioJournalSnapshotReloads(t, core)
}

func TestMultiRootAndParentLayerResolution(t *testing.T) {
	t.Parallel()
	tmp := t.TempDir()
	trunkA := filepath.Join(tmp, "trunkA")
	trunkB := filepath.Join(tmp, "trunkB")
	backing := filepath.Join(tmp, "backing")
	parentBacking := filepath.Join(tmp, "parent-backing")
	for _, d := range []string{trunkA, trunkB, backing, parentBacking} {
		testutil.FailErr(t, "mkdir", os.MkdirAll(d, 0o755))
	}

	testutil.FailErr(t, "write trunkA only_trunk.txt", os.WriteFile(filepath.Join(trunkA, "only_trunk.txt"), []byte("from-trunk-A"), 0o644))
	testutil.FailErr(t, "write trunkA overridden.txt", os.WriteFile(filepath.Join(trunkA, "overridden.txt"), []byte("trunk-version"), 0o644))
	testutil.FailErr(t, "write trunkB doc.txt", os.WriteFile(filepath.Join(trunkB, "doc.txt"), []byte("from-trunk-B"), 0o644))

	parentFileAbs := filepath.Join(parentBacking, "parent_overridden.blob")
	parentBytes := []byte("parent-checkpoint-version")
	testutil.FailErr(t, "write parent blob", os.WriteFile(parentFileAbs, parentBytes, 0o400))
	sum := sha256.Sum256(parentBytes)

	parentLayer := NewMemoryParentLayer("checkpoint-parent-1")
	parentLayer.SetEntry(QualifiedPath{RootID: "root-a", Rel: "overridden.txt"}, ParentEntry{
		Kind:       EntryRegular,
		Mode:       0o644,
		Size:       int64(len(parentBytes)),
		ModTime:    time.Now(),
		SHA256:     hex.EncodeToString(sum[:]),
		BackingAbs: parentFileAbs,
	})
	parentLayer.SetEntry(QualifiedPath{RootID: "root-a", Rel: "deleted_in_parent.txt"}, ParentEntry{
		Kind: EntryAbsent,
	})
	testutil.FailErr(t, "write trunkA deleted_in_parent.txt", os.WriteFile(filepath.Join(trunkA, "deleted_in_parent.txt"), []byte("should-be-masked"), 0o644))

	core, err := New(Config{
		ViewID:     "view-multi",
		BackingDir: backing,
		Roots: []RootSpec{
			{ID: "root-a", Label: "primary", TrunkPath: trunkA, IsPrimary: true},
			{ID: "root-b", Label: "docs", TrunkPath: trunkB, IsPrimary: false},
		},
		ParentLayers: []ParentLayer{parentLayer},
	})
	testutil.FailErr(t, "New multi-root Core", err)
	defer func() { _ = core.ReleaseView() }()

	// Resolve parent layer over trunk.
	gotOverridden, err := core.ReadFile(QualifiedPath{RootID: "root-a", Rel: "overridden.txt"})
	testutil.FailErr(t, "read overridden.txt from parent layer", err)
	if string(gotOverridden) != "parent-checkpoint-version" {
		t.Fatalf("expected parent checkpoint bytes, got %q", string(gotOverridden))
	}

	// Deleted in parent layer masks trunk.
	if _, err := core.Stat(QualifiedPath{RootID: "root-a", Rel: "deleted_in_parent.txt"}); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("expected deleted_in_parent.txt to be absent, got %v", err)
	}

	// Private mutation overrides parent layer while retaining parent layer as base!
	_, err = core.WriteFile(QualifiedPath{RootID: "root-a", Rel: "overridden.txt"}, []byte("private-worker-version"), 0o644)
	testutil.FailErr(t, "write private overridden.txt", err)
	base, ok := core.CapturedBase(QualifiedPath{RootID: "root-a", Rel: "overridden.txt"})
	if !ok || base.Layer != LayerParent || base.SHA256 != hex.EncodeToString(sum[:]) {
		t.Fatalf("expected base from LayerParent with matching SHA256, got %+v", base)
	}

	// Multi-root model path parsing and qualification.
	qpB, err := core.ParseModelPath("@docs/doc.txt")
	testutil.FailErr(t, "ParseModelPath @docs/doc.txt", err)
	if qpB.RootID != "root-b" || qpB.Rel != "doc.txt" {
		t.Fatalf("unexpected parsed path: %+v", qpB)
	}
	if disp := core.QualifyDisplay(qpB); disp != "@docs/doc.txt" {
		t.Fatalf("unexpected display path: %q", disp)
	}
	gotDocB, err := core.ReadFile(qpB)
	testutil.FailErr(t, "read @docs/doc.txt", err)
	if string(gotDocB) != "from-trunk-B" {
		t.Fatalf("unexpected @docs/doc.txt content: %q", string(gotDocB))
	}
}

func TestHandleLifecycleAndGitInterception(t *testing.T) {
	t.Parallel()
	core, trunk, _ := newSingleRootTestCore(t)
	qp := func(rel string) QualifiedPath {
		return QualifiedPath{RootID: "primary", Rel: rel}
	}

	// Seed a raw .git gitlink file in trunk (simulating a linked worktree or submodule).
	testutil.FailErr(t, "write trunk .git gitlink", os.WriteFile(filepath.Join(trunk, ".git"), []byte("gitdir: /outside/repo/.git/worktrees/w1\n"), 0o644))

	// Lookup of .git must never expose an unrewritten source gitlink while initialization is pending (#1657, #1671).
	if _, err := core.Stat(qp(".git")); !errors.Is(err, ErrGitPending) {
		t.Fatalf("expected ErrGitPending on uninitialized .git lookup, got %v", err)
	}
	if _, err := core.ReadFile(qp(".git")); !errors.Is(err, ErrGitPending) {
		t.Fatalf("expected ErrGitPending on uninitialized .git read, got %v", err)
	}
	if _, err := core.Stat(qp(".git/HEAD")); !errors.Is(err, ErrGitPending) {
		t.Fatalf("expected ErrGitPending on .git/HEAD lookup, got %v", err)
	}

	// Test handle survival across rename, refusal on identity reuse, and refusal after ReleaseView.
	testutil.FailErr(t, "write trunk handle_file.txt", os.WriteFile(filepath.Join(trunk, "handle_file.txt"), []byte("gen-1"), 0o644))
	hid, attr1, err := core.Open(qp("handle_file.txt"), OpenReadWrite, 0)
	testutil.FailErr(t, "open handle_file.txt", err)

	// Rename file while handle is open.
	testutil.FailErr(t, "rename handle_file.txt -> renamed_file.txt", core.Rename(qp("handle_file.txt"), qp("renamed_file.txt")))
	_, err = core.WriteHandle(hid, []byte("-updated"), 5)
	testutil.FailErr(t, "write through handle after rename", err)

	gotRenamed, err := core.ReadFile(qp("renamed_file.txt"))
	testutil.FailErr(t, "read renamed_file.txt", err)
	if string(gotRenamed) != "gen-1-updated" {
		t.Fatalf("unexpected content after handle write across rename: %q", string(gotRenamed))
	}

	// Create a new file at the old pathname (`handle_file.txt`) and replace `renamed_file.txt`:
	// old handle still points to `attr1.NodeID` + `attr1.Generation`, while new replacement gets a distinct NodeID/Generation.
	_, attr2, err := core.CreateFile(qp("handle_file.txt"), 0o644, true)
	testutil.FailErr(t, "recreate handle_file.txt", err)
	if attr2.NodeID == attr1.NodeID || attr2.Generation == attr1.Generation {
		t.Fatalf("expected distinct NodeID and Generation on recreated file, got attr1=%+v attr2=%+v", attr1, attr2)
	}

	// A descriptor opened to create a read-only file keeps its write access (#1660).
	roHID, _, err := core.CreateFile(qp("loose_obj"), 0o444, true)
	testutil.FailErr(t, "create 0444 loose_obj", err)
	_, err = core.WriteHandle(roHID, []byte("git-blob-bytes"), 0)
	testutil.FailErr(t, "write to the creating descriptor of a 0444 file", err)
	testutil.FailErr(t, "close loose_obj handle", core.CloseHandle(roHID))

	// Release view: held handle must be refused immediately.
	testutil.FailErr(t, "ReleaseView", core.ReleaseView())
	buf := make([]byte, 8)
	if _, err := core.ReadHandle(hid, buf, 0); !errors.Is(err, ErrViewReleased) {
		t.Fatalf("expected ErrViewReleased on ReadHandle after ReleaseView, got %v", err)
	}
	if MapErrno(ErrStaleHandle) != syscall.ESTALE {
		t.Fatalf("expected ESTALE for ErrStaleHandle")
	}
}

func scenarioXattrsAndAppleDouble(t *testing.T, core *Core, qp func(string) QualifiedPath) {
	t.Helper()
	// 10. Xattrs and legitimate ._* AppleDouble files
	testutil.FailErr(t, "setxattr user.test", core.SetXattr(qp("base.txt"), "user.test", []byte("xattr-val"), false))
	xval, err := core.GetXattr(qp("base.txt"), "user.test", false)
	testutil.FailErr(t, "getxattr user.test", err)
	if string(xval) != "xattr-val" {
		t.Fatalf("unexpected xattr value: %q", string(xval))
	}
	xnames, err := core.ListXattr(qp("base.txt"), false)
	testutil.FailErr(t, "listxattr base.txt", err)
	foundUserTest := false
	for _, xn := range xnames {
		if xn == "user.test" {
			foundUserTest = true
		}
	}
	if !foundUserTest {
		t.Fatalf("expected user.test in xattr names, got %v", xnames)
	}
	appleDouble := []byte{0x00, 0x05, 0x16, 0x07, 0x00, 0x02, 0x00, 0x00, 'm', 'a', 'c', 'o', 's'}
	_, err = core.WriteFile(qp("._base.txt"), appleDouble, 0o644)
	testutil.FailErr(t, "write ._base.txt AppleDouble file", err)
	gotAppleDouble, err := core.ReadFile(qp("._base.txt"))
	testutil.FailErr(t, "read ._base.txt", err)
	if !bytes.Equal(gotAppleDouble, appleDouble) {
		t.Fatalf("AppleDouble content was corrupted or dropped: %x", gotAppleDouble)
	}
}

func scenarioCopyUpWithoutChangeIsNotLogical(t *testing.T, core *Core, qp func(string) QualifiedPath) {
	t.Helper()
	// 11. Physical copy-up vs logical change: open-for-write without changing bytes or metadata
	roHandle, _, err := core.Open(qp("readonly_touch.txt"), OpenReadWrite, 0)
	testutil.FailErr(t, "open readonly_touch.txt O_RDWR", err)
	testutil.FailErr(t, "close readonly_touch.txt without writing", core.CloseHandle(roHandle))

	allChanges, err := core.EnumerateChanges(false)
	testutil.FailErr(t, "EnumerateChanges(false)", err)
	logicalChanges, err := core.EnumerateChanges(true)
	testutil.FailErr(t, "EnumerateChanges(true)", err)

	foundPhysicalTouch := false
	for _, ce := range allChanges {
		if ce.Path == qp("readonly_touch.txt") {
			foundPhysicalTouch = true
			if !ce.PhysicalCopyUp || ce.LogicalChange || ce.ContentChanged || ce.MetadataChanged {
				t.Fatalf("expected readonly_touch.txt to be physical copy-up only, got %+v", ce)
			}
		}
	}
	if !foundPhysicalTouch {
		t.Fatal("expected readonly_touch.txt in EnumerateChanges(false)")
	}
	for _, ce := range logicalChanges {
		if ce.Path == qp("readonly_touch.txt") {
			t.Fatalf("readonly_touch.txt must not appear in logical changes: %+v", ce)
		}
	}
}

func scenarioJournalSnapshotReloads(t *testing.T, core *Core) {
	t.Helper()
	// 12. Journal snapshot persistence and reload
	snap, err := core.SnapshotJournal()
	testutil.FailErr(t, "SnapshotJournal", err)
	if snap.FormatVersion != FormatVersion || len(snap.Operations) == 0 {
		t.Fatalf("unexpected journal snapshot: %+v", snap)
	}
	loaded, err := core.LoadPersistedJournal()
	testutil.FailErr(t, "LoadPersistedJournal", err)
	if loaded.FormatVersion != FormatVersion || len(loaded.Operations) != len(snap.Operations) {
		t.Fatalf("persisted journal mismatch: got %d ops, want %d", len(loaded.Operations), len(snap.Operations))
	}
}

func seedScenarioTrunk(t *testing.T, trunk string) {
	t.Helper()
	testutil.FailErr(t, "write trunk base.txt", os.WriteFile(filepath.Join(trunk, "base.txt"), []byte("initial-trunk-content\n"), 0o644))
	testutil.FailErr(t, "write trunk trunc.txt", os.WriteFile(filepath.Join(trunk, "trunc.txt"), []byte("0123456789abcdef"), 0o644))
	testutil.FailErr(t, "write trunk sed.txt", os.WriteFile(filepath.Join(trunk, "sed.txt"), []byte("hello world\n"), 0o644))
	testutil.FailErr(t, "write trunk replace_me", os.WriteFile(filepath.Join(trunk, "replace_me"), []byte("i am a file"), 0o644))
	testutil.FailErr(t, "mkdir trunk dir_src", os.MkdirAll(filepath.Join(trunk, "dir_src", "nested"), 0o755))
	testutil.FailErr(t, "write trunk dir_src/nested/item.txt", os.WriteFile(filepath.Join(trunk, "dir_src", "nested", "item.txt"), []byte("nested-item"), 0o644))
	testutil.FailErr(t, "write trunk open_unlink.txt", os.WriteFile(filepath.Join(trunk, "open_unlink.txt"), []byte("survive-unlink"), 0o644))
	testutil.FailErr(t, "write trunk hard_target.txt", os.WriteFile(filepath.Join(trunk, "hard_target.txt"), []byte("shared-bytes"), 0o644))
	testutil.FailErr(t, "write trunk readonly_touch.txt", os.WriteFile(filepath.Join(trunk, "readonly_touch.txt"), []byte("unchanged"), 0o644))
}
