package nfsserver_test

import (
	"bytes"
	"syscall"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/layeredfs"
	"github.com/lycaon/lycaon/internal/layeredfs/nfsserver"
	nfs "github.com/willscott/go-nfs"
)

func TestAllSupportedProceduresAndTypedFailures(t *testing.T) {
	var injectENOSPC bool
	env := newTestEnv(t, &nfsserver.FaultInjector{
		OnWrite: func(_ nfsserver.ViewID, _ layeredfs.QualifiedPath, _ uint64, _ int, _ nfsserver.WriteStability) error {
			if injectENOSPC {
				return syscall.ENOSPC
			}
			return nil
		},
	})
	capCfg := env.createViewAndExport(t, "view-all", true)
	cli := dialRPC(t, env.Server.Addr(), env.UID, env.GID)

	// MOUNT NULL, DUMP, EXPORT, MNT.
	if code, _ := cli.call(100005, 3, uint32(nfs.MountProcNull), nil); code != nfs.ResponseCodeSuccess {
		t.Fatalf("MOUNT NULL code: %v", code)
	}
	for _, proc := range []nfs.MountProcedure{nfs.MountProcDump, nfs.MountProcExport} {
		_, r := cli.call(100005, 3, uint32(proc), nil)
		if getU32(t, r) != 0 {
			t.Fatalf("MOUNT %v must return empty list", proc)
		}
	}
	st, rootFH := cli.mount(capCfg.ExportPath)
	if st != nfs.MountStatusOk || len(rootFH) != nfsserver.HandleSize {
		t.Fatalf("MOUNT failed: st=%v len=%d", st, len(rootFH))
	}

	// NFS NULL, GETATTR, ACCESS, FSSTAT, FSINFO, PATHCONF.
	if code, _ := cli.call(100003, 3, uint32(nfs.NFSProcedureNull), nil); code != nfs.ResponseCodeSuccess {
		t.Fatalf("NFS NULL code: %v", code)
	}
	if nst, attr := cli.getAttr(rootFH); nst != nfs.NFSStatusOk || attr.Type != nfs.FileTypeDirectory {
		t.Fatalf("GETATTR root: st=%v type=%v", nst, attr.Type)
	}
	var accArgs bytes.Buffer
	putBytes(&accArgs, rootFH)
	putU32(&accArgs, 0x3f)
	_, r := cli.call(100003, 3, uint32(nfs.NFSProcedureAccess), accArgs.Bytes())
	if nst := nfs.NFSStatus(getU32(t, r)); nst != nfs.NFSStatusOk {
		t.Fatalf("ACCESS: %v", nst)
	}
	for _, proc := range []nfs.NFSProcedure{nfs.NFSProcedureFSStat, nfs.NFSProcedureFSInfo, nfs.NFSProcedurePathConf} {
		var b bytes.Buffer
		putBytes(&b, rootFH)
		_, pr := cli.call(100003, 3, uint32(proc), b.Bytes())
		if nst := nfs.NFSStatus(getU32(t, pr)); nst != nfs.NFSStatusOk {
			t.Fatalf("proc %v failed: %v", proc, nst)
		}
	}

	// LOOKUP existing & ENOENT.
	if nst, _, _ := cli.lookup(rootFH, "missing.txt"); nst != nfs.NFSStatusNoEnt {
		t.Fatalf("expected ENOENT, got %v", nst)
	}
	nst, readmeFH, _ := cli.lookup(rootFH, "README.md")
	if nst != nfs.NFSStatusOk {
		t.Fatalf("LOOKUP README.md: %v", nst)
	}
	// ENOTDIR: LOOKUP on regular file.
	if nst, _, _ := cli.lookup(readmeFH, "child"); nst != nfs.NFSStatusNotDir {
		t.Fatalf("expected ENOTDIR, got %v", nst)
	}
	// EISDIR: READ on directory.
	if nst, _, _ := cli.read(rootFH, 0, 64); nst != nfs.NFSStatusIsDir {
		t.Fatalf("expected EISDIR, got %v", nst)
	}

	// CREATE (Unchecked, Guarded, Exclusive + idempotent retry) & EEXIST.
	verf := [8]byte{1, 2, 3, 4, 5, 6, 7, 8}
	nst, exclFH1 := cli.create(rootFH, "excl.txt", 0o644, 2, verf)
	if nst != nfs.NFSStatusOk {
		t.Fatalf("CREATE exclusive: %v", nst)
	}
	nst, exclFH2 := cli.create(rootFH, "excl.txt", 0o644, 2, verf)
	if nst != nfs.NFSStatusOk || !bytes.Equal(exclFH1, exclFH2) {
		t.Fatalf("CREATE exclusive retry failed: st=%v", nst)
	}
	if nst, _ := cli.create(rootFH, "excl.txt", 0o644, 1, verf); nst != nfs.NFSStatusExist {
		t.Fatalf("expected EEXIST on guarded create, got %v", nst)
	}

	// MKDIR, RMDIR, ENOTEMPTY.
	var mkdirArgs bytes.Buffer
	putBytes(&mkdirArgs, rootFH)
	putStr(&mkdirArgs, "sub")
	encodeSattrMode(&mkdirArgs, true, 0o755)
	_, mr := cli.call(100003, 3, uint32(nfs.NFSProcedureMkDir), mkdirArgs.Bytes())
	if nst := nfs.NFSStatus(getU32(t, mr)); nst != nfs.NFSStatusOk {
		t.Fatalf("MKDIR: %v", nst)
	}
	_ = getU32(t, mr)
	subFH := getBytes(t, mr)
	_, childFH := cli.create(subFH, "inside.txt", 0o644, 0, [8]byte{})

	var rmdirArgs bytes.Buffer
	putBytes(&rmdirArgs, rootFH)
	putStr(&rmdirArgs, "sub")
	_, rr := cli.call(100003, 3, uint32(nfs.NFSProcedureRmDir), rmdirArgs.Bytes())
	if nst := nfs.NFSStatus(getU32(t, rr)); nst != nfs.NFSStatusNotEmpty {
		t.Fatalf("expected ENOTEMPTY, got %v", nst)
	}

	// SYMLINK, READLINK, and ELOOP via Core symlink loop mapping.
	var symArgs bytes.Buffer
	putBytes(&symArgs, rootFH)
	putStr(&symArgs, "link-readme")
	encodeSattrMode(&symArgs, true, 0o777)
	putStr(&symArgs, "README.md")
	_, sr := cli.call(100003, 3, uint32(nfs.NFSProcedureSymlink), symArgs.Bytes())
	if nst := nfs.NFSStatus(getU32(t, sr)); nst != nfs.NFSStatusOk {
		t.Fatalf("SYMLINK: %v", nst)
	}
	_ = getU32(t, sr)
	symFH := getBytes(t, sr)
	var rlArgs bytes.Buffer
	putBytes(&rlArgs, symFH)
	_, rlr := cli.call(100003, 3, uint32(nfs.NFSProcedureReadlink), rlArgs.Bytes())
	if nst := nfs.NFSStatus(getU32(t, rlr)); nst != nfs.NFSStatusOk {
		t.Fatalf("READLINK: %v", nst)
	}
	_ = getPostOpAttr(t, rlr)
	if gotTarget := string(getBytes(t, rlr)); gotTarget != "README.md" {
		t.Fatalf("READLINK target=%q", gotTarget)
	}

	// MKNOD with its attributes missing is malformed; LINK creates hard link sharing NodeID and handle.
	var mknodArgs bytes.Buffer
	putBytes(&mknodArgs, rootFH)
	putStr(&mknodArgs, "fifo")
	putU32(&mknodArgs, 6)
	_, mkr := cli.call(100003, 3, uint32(nfs.NFSProcedureMkNod), mknodArgs.Bytes())
	if nst := nfs.NFSStatus(getU32(t, mkr)); nst != nfs.NFSStatusInval {
		t.Fatalf("MKNOD status=%v", nst)
	}
	var linkArgs bytes.Buffer
	putBytes(&linkArgs, childFH)
	putBytes(&linkArgs, rootFH)
	putStr(&linkArgs, "hardlink")
	_, lkr := cli.call(100003, 3, uint32(nfs.NFSProcedureLink), linkArgs.Bytes())
	if nst := nfs.NFSStatus(getU32(t, lkr)); nst != nfs.NFSStatusOk {
		t.Fatalf("LINK status=%v", nst)
	}
	linkPostAttr := getPostOpAttr(t, lkr)
	if linkPostAttr.Nlink != 2 {
		t.Fatalf("expected Nlink=2 after LINK, got %d", linkPostAttr.Nlink)
	}
	nst, linkFH, linkLookupAttr := cli.lookup(rootFH, "hardlink")
	if nst != nfs.NFSStatusOk || !bytes.Equal(linkFH, childFH) || linkLookupAttr.Fileid != linkPostAttr.Fileid || linkLookupAttr.Nlink != 2 {
		t.Fatalf("hardlink lookup mismatch: st=%v sameFH=%v fileid=%d/%d nlink=%d",
			nst, bytes.Equal(linkFH, childFH), linkLookupAttr.Fileid, linkPostAttr.Fileid, linkLookupAttr.Nlink)
	}
	if nst, _, _, _ := cli.write(linkFH, 0, []byte("shared-hardlink-data"), nfsserver.FileSync); nst != nfs.NFSStatusOk {
		t.Fatalf("write via hardlink: %v", nst)
	}
	var remLink bytes.Buffer
	putBytes(&remLink, rootFH)
	putStr(&remLink, "hardlink")
	_, rlr2 := cli.call(100003, 3, uint32(nfs.NFSProcedureRemove), remLink.Bytes())
	if nst := nfs.NFSStatus(getU32(t, rlr2)); nst != nfs.NFSStatusOk {
		t.Fatalf("REMOVE hardlink: %v", nst)
	}
	if nst, gotShared, _ := cli.read(childFH, 0, 64); nst != nfs.NFSStatusOk || string(gotShared) != "shared-hardlink-data" {
		t.Fatalf("read original after hardlink remove: st=%v data=%q", nst, gotShared)
	}

	// ENOSPC on WRITE.
	injectENOSPC = true
	if nst, _, _, _ := cli.write(childFH, 0, []byte("x"), nfsserver.FileSync); nst != nfs.NFSStatusNoSPC {
		t.Fatalf("expected ENOSPC, got %v", nst)
	}
	injectENOSPC = false

	// SETATTR with foreign UID -> EPERM. WRITE follows the handle, not the current mode.
	var setUIDArgs bytes.Buffer
	putBytes(&setUIDArgs, readmeFH)
	for _, v := range []uint32{0, 1, env.UID + 999, 0, 0, 0, 0, 0} {
		putU32(&setUIDArgs, v)
	}
	_, sur := cli.call(100003, 3, uint32(nfs.NFSProcedureSetAttr), setUIDArgs.Bytes())
	if nst := nfs.NFSStatus(getU32(t, sur)); nst != nfs.NFSStatusPerm {
		t.Fatalf("expected EPERM(NFSStatusPerm), got %v", nst)
	}
	_, _ = env.Core.Chmod(layeredfs.QualifiedPath{RootID: nfsserver.ProjectRoot, Rel: "README.md"}, 0o444)
	if nst, _, _, _ := cli.write(readmeFH, 0, []byte("kept"), nfsserver.FileSync); nst != nfs.NFSStatusOk {
		t.Fatalf("expected WRITE through a held handle to succeed after chmod, got %v", nst)
	}
	if nfsserver.MapErrorToNFSStatus(syscall.ELOOP) != nfsserver.NFSStatusLoop {
		t.Fatal("expected ELOOP to map to NFSStatusLoop")
	}
	roCap := env.createViewAndExport(t, "view-ro", false)
	roCli := dialRPC(t, env.Server.Addr(), env.UID, env.GID)
	_, roRoot := roCli.mount(roCap.ExportPath)
	if nst, _ := roCli.create(roRoot, "nope.txt", 0o644, 0, [8]byte{}); nst != nfs.NFSStatusROFS {
		t.Fatalf("expected EROFS, got %v", nst)
	}
	for proc := nfs.NFSProcedureGetAttr; proc <= nfs.NFSProcedureCommit; proc++ {
		_, mr := cli.call(100003, 3, uint32(proc), []byte{0, 0})
		if nst := nfs.NFSStatus(getU32(t, mr)); nst != nfs.NFSStatusInval {
			t.Fatalf("malformed proc %v: got %v, want NFSStatusInval", proc, nst)
		}
	}

	// REMOVE inside.txt and RMDIR sub.
	var remArgs bytes.Buffer
	putBytes(&remArgs, subFH)
	putStr(&remArgs, "inside.txt")
	if _, rmr := cli.call(100003, 3, uint32(nfs.NFSProcedureRemove), remArgs.Bytes()); nfs.NFSStatus(getU32(t, rmr)) != nfs.NFSStatusOk {
		t.Fatal("REMOVE inside.txt failed")
	}
	if _, rr2 := cli.call(100003, 3, uint32(nfs.NFSProcedureRmDir), rmdirArgs.Bytes()); nfs.NFSStatus(getU32(t, rr2)) != nfs.NFSStatusOk {
		t.Fatal("RMDIR empty sub failed")
	}
	var umntArgs bytes.Buffer
	putStr(&umntArgs, roCap.ExportPath)
	if code, _ := roCli.call(100005, 3, uint32(nfs.MountProcUmnt), umntArgs.Bytes()); code != nfs.ResponseCodeSuccess {
		t.Fatalf("UMNT failed: %v", code)
	}
	if code, _ := cli.call(100005, 3, uint32(nfs.MountProcUmntAll), nil); code != nfs.ResponseCodeSuccess {
		t.Fatalf("UMNTALL failed: %v", code)
	}
}

func TestGitLooseObject0444AndPOSIXMetadataPreservation(t *testing.T) {
	env := newTestEnv(t, nil)
	capCfg := env.createViewAndExport(t, "view-git", true)
	cli := dialRPC(t, env.Server.Addr(), env.UID, env.GID)
	_, rootFH := cli.mount(capCfg.ExportPath)

	// Git creates loose object with mode 0444, writes compressed object bytes, commits, and renames.
	nst, tmpFH := cli.create(rootFH, "tmp_obj_abc123", 0o444, 1, [8]byte{})
	if nst != nfs.NFSStatusOk {
		t.Fatalf("CREATE 0444 loose object: %v", nst)
	}
	payload := []byte("blob 12\x00hello world\n")
	if nst, _, _, _ := cli.write(tmpFH, 0, payload, nfsserver.Unstable); nst != nfs.NFSStatusOk {
		t.Fatalf("WRITE to 0444 loose object: %v", nst)
	}
	if nst, _ := cli.commit(tmpFH, 0, uint32(len(payload))); nst != nfs.NFSStatusOk {
		t.Fatalf("COMMIT 0444 loose object: %v", nst)
	}
	var renArgs bytes.Buffer
	putBytes(&renArgs, rootFH)
	putStr(&renArgs, "tmp_obj_abc123")
	putBytes(&renArgs, rootFH)
	putStr(&renArgs, "final_obj_abc123")
	if _, rr := cli.call(100003, 3, uint32(nfs.NFSProcedureRename), renArgs.Bytes()); nfs.NFSStatus(getU32(t, rr)) != nfs.NFSStatusOk {
		t.Fatal("RENAME 0444 loose object failed")
	}
	if nst, attr := cli.getAttr(tmpFH); nst != nfs.NFSStatusOk || attr.FileMode != 0o444 || attr.Filesize != uint64(len(payload)) {
		t.Fatalf("loose object attr mismatch: st=%v mode=%o size=%d", nst, attr.FileMode, attr.Filesize)
	}
	for _, jop := range env.Core.JournalOperations() {
		if jop.Kind == layeredfs.JournalOpSetAttr {
			t.Fatalf("unexpected spurious JournalOpSetAttr during 0444 loose object write: %+v", jop)
		}
	}

	// cp -p / tar -xpf / rsync -a / install -m 0644: set explicit mode + client atime/mtime + truncate size.
	nst, metaFH := cli.create(rootFH, "installed.bin", 0o755, 0, [8]byte{})
	if nst != nfs.NFSStatusOk {
		t.Fatalf("CREATE installed.bin: %v", nst)
	}
	_, _, _, _ = cli.write(metaFH, 0, []byte("0123456789"), nfsserver.FileSync)
	wantTime := time.Date(2025, 6, 15, 12, 30, 0, 123000000, time.UTC)
	var sa bytes.Buffer
	putBytes(&sa, metaFH)
	for _, v := range []uint32{1, 0o644, 1, env.UID, 1, env.GID, 1} {
		putU32(&sa, v)
	}
	putU64(&sa, 4)
	for _, v := range []uint32{
		2, uint32(wantTime.Unix()), uint32(wantTime.Nanosecond()),
		2, uint32(wantTime.Unix()), uint32(wantTime.Nanosecond()), 0,
	} {
		putU32(&sa, v)
	}
	_, sar := cli.call(100003, 3, uint32(nfs.NFSProcedureSetAttr), sa.Bytes())
	if nst := nfs.NFSStatus(getU32(t, sar)); nst != nfs.NFSStatusOk {
		t.Fatalf("SETATTR metadata preservation: %v", nst)
	}
	nst, gotAttr := cli.getAttr(metaFH)
	if nst != nfs.NFSStatusOk || gotAttr.FileMode != 0o644 || gotAttr.Filesize != 4 ||
		gotAttr.Mtime.Seconds != uint32(wantTime.Unix()) {
		t.Fatalf("preserved metadata mismatch: %+v", gotAttr)
	}
}
