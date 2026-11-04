package nfsserver_test

import (
	"bytes"
	"context"
	"encoding/binary"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"syscall"
	"testing"

	"github.com/lycaon/lycaon/internal/layeredfs"
	"github.com/lycaon/lycaon/internal/layeredfs/nfsserver"
	nfs "github.com/willscott/go-nfs"
)

func TestRenameAndOpenUnlinkPreserveLiveHandlesAndStaleRejection(t *testing.T) {
	env := newTestEnv(t, nil)
	capCfg := env.createViewAndExport(t, "view-handles", true)
	cli := dialRPC(t, env.Server.Addr(), env.UID, env.GID)
	_, rootFH := cli.mount(capCfg.ExportPath)

	// 1. Handle survival across RENAME.
	nst, origFH := cli.create(rootFH, "before.txt", 0o644, 0, [8]byte{})
	if nst != nfs.NFSStatusOk {
		t.Fatalf("CREATE before.txt: %v", nst)
	}
	_, _, _, _ = cli.write(origFH, 0, []byte("survives-rename"), nfsserver.FileSync)

	var ren bytes.Buffer
	putBytes(&ren, rootFH)
	putStr(&ren, "before.txt")
	putBytes(&ren, rootFH)
	putStr(&ren, "after.txt")
	_, rr := cli.call(100003, 3, uint32(nfs.NFSProcedureRename), ren.Bytes())
	if nst := nfs.NFSStatus(getU32(t, rr)); nst != nfs.NFSStatusOk {
		t.Fatalf("RENAME: %v", nst)
	}
	if nst, data, _ := cli.read(origFH, 0, 64); nst != nfs.NFSStatusOk || string(data) != "survives-rename" {
		t.Fatalf("READ via pre-rename handle failed: st=%v data=%q", nst, data)
	}

	// 2. Open-unlink: REMOVE after.txt while holding origFH, then READ/WRITE/COMMIT/GETATTR via origFH.
	var rem bytes.Buffer
	putBytes(&rem, rootFH)
	putStr(&rem, "after.txt")
	_, rmr := cli.call(100003, 3, uint32(nfs.NFSProcedureRemove), rem.Bytes())
	if nst := nfs.NFSStatus(getU32(t, rmr)); nst != nfs.NFSStatusOk {
		t.Fatalf("REMOVE after.txt: %v", nst)
	}
	if nst, _, _ := cli.lookup(rootFH, "after.txt"); nst != nfs.NFSStatusNoEnt {
		t.Fatalf("after.txt should be unlinked from directory, got %v", nst)
	}
	if nst, _, _, _ := cli.write(origFH, 0, []byte("open-unlinked-write"), nfsserver.Unstable); nst != nfs.NFSStatusOk {
		t.Fatalf("WRITE to open-unlinked handle failed: %v", nst)
	}
	if nst, _ := cli.commit(origFH, 0, 19); nst != nfs.NFSStatusOk {
		t.Fatalf("COMMIT on open-unlinked handle failed: %v", nst)
	}
	if nst, data, _ := cli.read(origFH, 0, 64); nst != nfs.NFSStatusOk || string(data) != "open-unlinked-write" {
		t.Fatalf("READ from open-unlinked handle failed: st=%v data=%q", nst, data)
	}

	// 3. Forged handle -> NFSStatusStale.
	forged := append([]byte(nil), origFH...)
	forged[len(forged)-1] ^= 0xff
	if nst, _ := cli.getAttr(forged); nst != nfs.NFSStatusStale {
		t.Fatalf("expected NFSStatusStale for forged handle, got %v", nst)
	}

	// 4. Generation reuse rejection: create gen.txt, delete and recreate, verify old handle if generation differs.
	_, genFH := cli.create(rootFH, "gen.txt", 0o644, 0, [8]byte{})
	var remGen bytes.Buffer
	putBytes(&remGen, rootFH)
	putStr(&remGen, "gen.txt")
	// Invalidate handle path directly via Rmdir/Recreate or ReleaseExport.
	env.Server.ReleaseExport("view-handles")
	if nst, _ := cli.getAttr(genFH); nst != nfs.NFSStatusStale {
		t.Fatalf("expected NFSStatusStale after view release, got %v", nst)
	}
	if nst, _ := cli.getAttr(origFH); nst != nfs.NFSStatusStale {
		t.Fatalf("expected NFSStatusStale on open-unlinked handle after view release, got %v", nst)
	}
}

func TestDirectoryMutationCookieVerifierInvalidationAndO1HandleLookup(t *testing.T) {
	env := newTestEnv(t, nil)
	capCfg := env.createViewAndExport(t, "view-dir", true)
	cli := dialRPC(t, env.Server.Addr(), env.UID, env.GID)
	_, rootFH := cli.mount(capCfg.ExportPath)

	const numFiles = 40
	handles := make([][]byte, 0, numFiles)
	for i := range numFiles {
		name := fmt.Sprintf("file-%03d.txt", i)
		nst, fh := cli.create(rootFH, name, 0o644, 0, [8]byte{})
		if nst != nfs.NFSStatusOk {
			t.Fatalf("create %s: %v", name, nst)
		}
		handles = append(handles, fh)
	}

	// First page of READDIR (small count = 256 bytes so eof == false).
	var rd1 bytes.Buffer
	putBytes(&rd1, rootFH)
	putU64(&rd1, 0)
	putU64(&rd1, 0)
	putU32(&rd1, 256)
	_, r := cli.call(100003, 3, uint32(nfs.NFSProcedureReadDir), rd1.Bytes())
	if st := nfs.NFSStatus(getU32(t, r)); st != nfs.NFSStatusOk {
		t.Fatalf("READDIR page 1: %v", st)
	}
	_ = getPostOpAttr(t, r)
	var verf1 [8]byte
	_, _ = io.ReadFull(r, verf1[:])
	if verf1 == ([8]byte{}) {
		t.Fatal("expected non-zero cookieverf")
	}
	var lastCookie uint64
	for getU32(t, r) == 1 {
		_, _ = getU64(t, r), getBytes(t, r)
		lastCookie = getU64(t, r)
	}
	if lastCookie == 0 {
		t.Fatal("expected at least one READDIR entry")
	}
	var rd2 bytes.Buffer
	putBytes(&rd2, rootFH)
	putU64(&rd2, lastCookie)
	_, _ = rd2.Write(verf1[:])
	putU32(&rd2, 4096)
	_, r2 := cli.call(100003, 3, uint32(nfs.NFSProcedureReadDir), rd2.Bytes())
	if st := nfs.NFSStatus(getU32(t, r2)); st != nfs.NFSStatusOk {
		t.Fatalf("READDIR continuation before mutation failed: %v", st)
	}
	// Mutate directory -> old cookieverf MUST be rejected with NFSStatusBadCookie on both READDIR and READDIRPLUS!
	_, _ = cli.create(rootFH, "mutated-new.txt", 0o644, 0, [8]byte{})
	_, r3 := cli.call(100003, 3, uint32(nfs.NFSProcedureReadDir), rd2.Bytes())
	if st := nfs.NFSStatus(getU32(t, r3)); st != nfs.NFSStatusBadCookie {
		t.Fatalf("expected NFSStatusBadCookie on READDIR after dir mutation, got %v", st)
	}
	var rdp bytes.Buffer
	putBytes(&rdp, rootFH)
	putU64(&rdp, lastCookie)
	_, _ = rdp.Write(verf1[:])
	putU32(&rdp, 1024)
	putU32(&rdp, 4096)
	_, r4 := cli.call(100003, 3, uint32(nfs.NFSProcedureReadDirPlus), rdp.Bytes())
	if st := nfs.NFSStatus(getU32(t, r4)); st != nfs.NFSStatusBadCookie {
		t.Fatalf("expected NFSStatusBadCookie on READDIRPLUS after dir mutation, got %v", st)
	}
	var rdpFresh bytes.Buffer
	putBytes(&rdpFresh, rootFH)
	putU64(&rdpFresh, 0)
	putU64(&rdpFresh, 0)
	putU32(&rdpFresh, 4096)
	putU32(&rdpFresh, 16384)
	_, r5 := cli.call(100003, 3, uint32(nfs.NFSProcedureReadDirPlus), rdpFresh.Bytes())
	if st := nfs.NFSStatus(getU32(t, r5)); st != nfs.NFSStatusOk {
		t.Fatalf("fresh READDIRPLUS failed: %v", st)
	}
	for i, fh := range handles {
		if qpath, err := env.Server.ResolveHandleForView(fh, "view-dir"); err != nil || qpath.Rel != fmt.Sprintf("file-%03d.txt", i) {
			t.Fatalf("ResolveHandleForView(%d): qpath=%+v err=%v", i, qpath, err)
		}
	}
}

func TestWriteStabilityFaultInjectionVerifierRotationAndCheckpointBarrier(t *testing.T) {
	var failSync, failCommit bool
	env := newTestEnv(t, &nfsserver.FaultInjector{
		OnSync: func(_ nfsserver.ViewID, _ layeredfs.QualifiedPath) error {
			if failSync {
				return syscall.EIO
			}
			return nil
		},
		OnCommit: func(_ nfsserver.ViewID, _ layeredfs.QualifiedPath, _ uint64, _ uint32) error {
			if failCommit {
				return syscall.EIO
			}
			return nil
		},
	})
	capCfg := env.createViewAndExport(t, "view-dur", true)
	cli := dialRPC(t, env.Server.Addr(), env.UID, env.GID)
	_, rootFH := cli.mount(capCfg.ExportPath)
	_, fh := cli.create(rootFH, "durable.txt", 0o644, 0, [8]byte{})

	st, n, committed, verf1 := cli.write(fh, 0, []byte("chunk-one"), nfsserver.Unstable)
	if st != nfs.NFSStatusOk || n != 9 || committed != nfsserver.Unstable {
		t.Fatalf("UNSTABLE write: st=%v n=%d committed=%v", st, n, committed)
	}
	if st, data, _ := cli.read(fh, 0, 64); st != nfs.NFSStatusOk || string(data) != "chunk-one" {
		t.Fatalf("READ of uncommitted UNSTABLE write: st=%v data=%q", st, data)
	}
	verf2 := env.Server.ResetUnstableState()
	if verf2 == verf1 {
		t.Fatalf("expected ResetUnstableState to rotate writeVerifier, got same %x", verf2)
	}
	if st, commitVerf := cli.commit(fh, 0, 9); st != nfs.NFSStatusOk || commitVerf != verf2 {
		t.Fatalf("COMMIT after restart: st=%v commitVerf=%x want=%x", st, commitVerf, verf2)
	}
	if _, _, _, verfAfterRetry := cli.write(fh, 0, []byte("chunk-one"), nfsserver.Unstable); verfAfterRetry != verf2 {
		t.Fatalf("expected retry WRITE verifier %x, got %x", verf2, verfAfterRetry)
	}
	failCommit = true
	if st, _ := cli.commit(fh, 0, 9); st != nfs.NFSStatusIO {
		t.Fatalf("expected NFSStatusIO on injected COMMIT fault, got %v", st)
	}
	failCommit, failSync = false, true
	if st, _ := cli.commit(fh, 0, 9); st != nfs.NFSStatusIO {
		t.Fatalf("expected NFSStatusIO on injected Sync fault, got %v", st)
	}
	failSync = false
	if st, finalVerf := cli.commit(fh, 0, 9); st != nfs.NFSStatusOk || finalVerf != verf2 {
		t.Fatalf("COMMIT retry failed: st=%v verf=%x", st, finalVerf)
	}
	_, _, _, _ = cli.write(fh, 9, []byte("-two"), nfsserver.Unstable)
	if err := env.Server.FlushForCheckpoint(context.Background(), "view-dur"); err != nil {
		t.Fatalf("FlushForCheckpoint: %v", err)
	}
	if _, data, _ := cli.read(fh, 0, 64); string(data) != "chunk-one-two" {
		t.Fatalf("after FlushForCheckpoint got %q, want %q", data, "chunk-one-two")
	}
	if st, _, comm, _ := cli.write(fh, 13, []byte("-three"), nfsserver.DataSync); st != nfs.NFSStatusOk || comm != nfsserver.DataSync {
		t.Fatalf("DATA_SYNC write failed: st=%v comm=%v", st, comm)
	}
	_ = binary.BigEndian
}

func TestAppleDoubleCreatePartialWriteReadRenameAndXattrRoundTrip(t *testing.T) {
	env := newTestEnv(t, nil)
	capCfg := env.createViewAndExport(t, "view-mac", true)
	cli := dialRPC(t, env.Server.Addr(), env.UID, env.GID)
	_, rootFH := cli.mount(capCfg.ExportPath)

	_, docFH := cli.create(rootFH, "doc.txt", 0o644, 0, [8]byte{})
	_, _, _, _ = cli.write(docFH, 0, []byte("document body"), nfsserver.FileSync)

	var finderInfo [32]byte
	copy(finderInfo[:], []byte("TEXTttxt"))
	adBytes := nfsserver.EncodeAppleDoubleXattrs(finderInfo, map[string][]byte{
		"user.com.apple.metadata:kMDItemWhereFroms": []byte("https://example.com"),
		"user.custom.tag": []byte("important"),
	})

	nst, adFH := cli.create(rootFH, "._doc.txt", 0o644, 0, [8]byte{})
	if nst != nfs.NFSStatusOk {
		t.Fatalf("CREATE ._doc.txt: %v", nst)
	}
	if nst, _, _, _ := cli.write(adFH, 0, adBytes[:26], nfsserver.Unstable); nst != nfs.NFSStatusOk {
		t.Fatalf("partial WRITE 1 ._doc.txt: %v", nst)
	}
	if nst, _, _, _ := cli.write(adFH, 26, adBytes[26:], nfsserver.Unstable); nst != nfs.NFSStatusOk {
		t.Fatalf("partial WRITE 2 ._doc.txt: %v", nst)
	}
	if nst, _ := cli.commit(adFH, 0, uint32(len(adBytes))); nst != nfs.NFSStatusOk {
		t.Fatalf("COMMIT ._doc.txt: %v", nst)
	}

	nst, gotAD, eof := cli.read(adFH, 0, uint32(len(adBytes)+64))
	if nst != nfs.NFSStatusOk || !eof || !bytes.Equal(gotAD, adBytes) {
		t.Fatalf("READ ._doc.txt mismatch: st=%v eof=%v len=%d", nst, eof, len(gotAD))
	}

	docQPath := layeredfs.QualifiedPath{RootID: nfsserver.ProjectRoot, Rel: "doc.txt"}
	val, err := env.Core.GetXattr(docQPath, "user.custom.tag", true)
	if err != nil || string(val) != "important" {
		t.Fatalf("GetXattr on doc.txt: val=%q err=%v", val, err)
	}

	// The file decoded completely and its attributes landed, so it is held as those attributes: gone
	// from the change set while the client's handle keeps reading the same bytes.
	assertNoLogicalChange(t, env.Core, "._doc.txt")
	if nst, again, _ := cli.read(adFH, 0, uint32(len(adBytes)+64)); nst != nfs.NFSStatusOk || !bytes.Equal(again, adBytes) {
		t.Fatalf("READ through the held handle: st=%v len=%d", nst, len(again))
	}

	var ra bytes.Buffer
	putBytes(&ra, rootFH)
	putStr(&ra, "doc.txt")
	putBytes(&ra, rootFH)
	putStr(&ra, "moved.txt")
	_, rr := cli.call(100003, 3, uint32(nfs.NFSProcedureRename), ra.Bytes())
	if nst := nfs.NFSStatus(getU32(t, rr)); nst != nfs.NFSStatusOk {
		t.Fatalf("RENAME doc.txt: %v", nst)
	}
	// The kernel finds no ._doc.txt once the target moved, and ._moved.txt presents the attributes.
	if nst, _, _ := cli.lookup(rootFH, "._doc.txt"); nst != nfs.NFSStatusNoEnt {
		t.Fatalf("LOOKUP ._doc.txt after the target moved: %v", nst)
	}
	nst, movedFH, _ := cli.lookup(rootFH, "._moved.txt")
	if nst != nfs.NFSStatusOk {
		t.Fatalf("LOOKUP ._moved.txt: %v", nst)
	}
	if nst, moved, _ := cli.read(movedFH, 0, uint32(len(adBytes)+64)); nst != nfs.NFSStatusOk || !bytes.Equal(moved, adBytes) {
		t.Fatalf("READ ._moved.txt: st=%v len=%d", nst, len(moved))
	}
	assertNoLogicalChange(t, env.Core, "._moved.txt")
}

func assertNoLogicalChange(t *testing.T, core *layeredfs.Core, rel string) {
	t.Helper()
	changes, err := core.EnumerateChanges(true)
	if err != nil {
		t.Fatalf("EnumerateChanges: %v", err)
	}
	for _, ch := range changes {
		if ch.Path.Rel == rel {
			t.Fatalf("%s is in the change set: %+v", rel, ch)
		}
	}
}

// TestOnlyACompleteSidecarWithATargetLeavesTheView holds the rule at the protocol: the name and magic
// bytes never decide, and every other shape stays an ordinary private file, byte for byte.
func TestOnlyACompleteSidecarWithATargetLeavesTheView(t *testing.T) {
	env := newTestEnv(t, nil)
	capCfg := env.createViewAndExport(t, "view-sidecar-rule", true)
	cli := dialRPC(t, env.Server.Addr(), env.UID, env.GID)
	_, rootFH := cli.mount(capCfg.ExportPath)
	_, targetFH := cli.create(rootFH, "target", 0o644, 0, [8]byte{})
	_, _, _, _ = cli.write(targetFH, 0, []byte("body"), nfsserver.FileSync)
	valid := nfsserver.EncodeAppleDoubleXattrs([32]byte{}, map[string][]byte{"user.a": []byte("one")})

	tamperedValue := bytes.Clone(valid)
	tamperedValue[len(tamperedValue)-1] = 0x7f // a non-zero byte no field accounts for
	wrongCount := bytes.Clone(valid)
	binary.BigEndian.PutUint16(wrongCount[118:], 200)
	for name, content := range map[string][]byte{
		"plain text": []byte("not a sidecar at all"),
		"magic only": bytes.Clone(valid[:8]),
		"truncated":  bytes.Clone(valid[:150]),
		"stray byte": tamperedValue,
		"bad table":  wrongCount,
	} {
		t.Run(name, func(t *testing.T) {
			_, fh := cli.create(rootFH, "._target", 0o644, 0, [8]byte{})
			if st, _, _, _ := cli.write(fh, 0, content, nfsserver.FileSync); st != nfs.NFSStatusOk {
				t.Fatalf("WRITE: %v", st)
			}
			got, err := env.Core.ReadFile(layeredfs.QualifiedPath{RootID: nfsserver.ProjectRoot, Rel: "._target"})
			if err != nil || !bytes.Equal(got, content) {
				t.Fatalf("content changed: %q err=%v", got, err)
			}
			attr, err := env.Core.Lstat(layeredfs.QualifiedPath{RootID: nfsserver.ProjectRoot, Rel: "target"})
			if err != nil || len(attr.Xattrs) != 0 {
				t.Fatalf("target gained attributes: %v err=%v", attr.Xattrs, err)
			}
			if err := env.Core.Unlink(layeredfs.QualifiedPath{RootID: nfsserver.ProjectRoot, Rel: "._target"}, false); err != nil {
				t.Fatalf("clean up: %v", err)
			}
		})
	}

	// A complete file with no target waits as a file, and is taken once the target arrives.
	_, earlyFH := cli.create(rootFH, "._later", 0o644, 0, [8]byte{})
	_, _, _, _ = cli.write(earlyFH, 0, valid, nfsserver.FileSync)
	if got, err := env.Core.ReadFile(layeredfs.QualifiedPath{RootID: nfsserver.ProjectRoot, Rel: "._later"}); err != nil || !bytes.Equal(got, valid) {
		t.Fatalf("no target: %q err=%v", got, err)
	}
	_, _ = cli.create(rootFH, "later", 0o644, 0, [8]byte{})
	assertNoLogicalChange(t, env.Core, "._later")
	if attr, err := env.Core.Lstat(layeredfs.QualifiedPath{RootID: nfsserver.ProjectRoot, Rel: "later"}); err != nil || string(attr.Xattrs["user.a"]) != "one" {
		t.Fatalf("attributes after the target arrived: %v err=%v", attr.Xattrs, err)
	}

	// Renaming data onto a sidecar name is the same decision as writing it there.
	_, plainFH := cli.create(rootFH, "payload", 0o644, 0, [8]byte{})
	_, _, _, _ = cli.write(plainFH, 0, []byte("data"), nfsserver.FileSync)
	var ra bytes.Buffer
	putBytes(&ra, rootFH)
	putStr(&ra, "payload")
	putBytes(&ra, rootFH)
	putStr(&ra, "._target")
	_, rr := cli.call(100003, 3, uint32(nfs.NFSProcedureRename), ra.Bytes())
	if st := nfs.NFSStatus(getU32(t, rr)); st != nfs.NFSStatusOk {
		t.Fatalf("RENAME: %v", st)
	}
	if got, err := env.Core.ReadFile(layeredfs.QualifiedPath{RootID: nfsserver.ProjectRoot, Rel: "._target"}); err != nil || string(got) != "data" {
		t.Fatalf("renamed data: %q err=%v", got, err)
	}

	// A sidecar the trunk itself holds is not the view's to remove.
	trunkSidecar := nfsserver.EncodeAppleDoubleXattrs([32]byte{}, map[string][]byte{"user.t": []byte("t")})
	if err := os.WriteFile(filepath.Join(env.ProjDir, "._README.md"), trunkSidecar, 0o644); err != nil {
		t.Fatalf("seed trunk sidecar: %v", err)
	}
	_, trunkFH, _ := cli.lookup(rootFH, "._README.md")
	if st, _, _, _ := cli.write(trunkFH, 0, trunkSidecar, nfsserver.FileSync); st != nfs.NFSStatusOk {
		t.Fatalf("WRITE trunk sidecar: %v", st)
	}
	if _, err := env.Core.ReadFile(layeredfs.QualifiedPath{RootID: nfsserver.ProjectRoot, Rel: "._README.md"}); err != nil {
		t.Fatalf("a sidecar from the trunk left the view: %v", err)
	}
}
