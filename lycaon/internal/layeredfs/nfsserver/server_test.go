package nfsserver_test

import (
	"bytes"
	"testing"

	"github.com/lycaon/lycaon/internal/layeredfs/nfsserver"
	nfs "github.com/willscott/go-nfs"
)

func (c *rpcClient) umount(exportPath string) {
	c.t.Helper()
	var args bytes.Buffer
	putStr(&args, exportPath)
	code, _ := c.call(100005, 3, uint32(nfs.MountProcUmnt), args.Bytes())
	if code != nfs.ResponseCodeSuccess {
		c.t.Fatalf("UMNT accept code: %v", code)
	}
}

func TestMountHandshakeAllowsRootUserlandWithValidCapabilityWhileNFSRequiresAllowedUID(t *testing.T) {
	t.Parallel()
	env := newTestEnv(t, nil)

	allowedUID := uint32(501)
	allowedGID := uint32(20)
	if env.UID == 0 {
		allowedUID = 1001
		allowedGID = 1001
	}

	capCfg, err := env.Server.RegisterExportWithUID("v-setuid-mount", nfsserver.ProjectRoot, "", allowedUID, allowedGID)
	if err != nil {
		t.Fatalf("RegisterExportWithUID: %v", err)
	}

	// 1. Setuid-root userland mount helper (UID=0, GID=0) with a wrong/guessed token must fail with MNT3ERR_ACCES.
	badRootMnt := dialRPC(t, env.Server.Addr(), 0, 0)
	if st, _ := badRootMnt.mount("/views/v-setuid-mount/project/wrong-token"); st != nfs.MountStatusErrAcces {
		t.Fatalf("expected MNT3ERR_ACCES for root UID=0 with wrong capability token, got %v", st)
	}

	// 2. Non-root caller with wrong UID/GID must still fail MOUNTPROC3_MNT even with valid capability token.
	wrongUserMnt := dialRPC(t, env.Server.Addr(), allowedUID+7, allowedGID+7)
	if st, _ := wrongUserMnt.mount(capCfg.ExportPath); st != nfs.MountStatusErrAcces {
		t.Fatalf("expected MNT3ERR_ACCES for non-root wrong UID/GID, got %v", st)
	}

	// 3. Setuid-root userland mount helper (UID=0, GID=0) with the valid capability token succeeds on MOUNTPROC3_MNT.
	rootMnt := dialRPC(t, env.Server.Addr(), 0, 0)
	st, rootFH := rootMnt.mount(capCfg.ExportPath)
	if st != nfs.MountStatusOk || len(rootFH) != nfsserver.HandleSize {
		t.Fatalf("expected MountStatusOk for UID=0,GID=0 with valid capability token, got st=%v len(fh)=%d", st, len(rootFH))
	}
	_ = rootMnt.conn.Close()
	if obs, ok := env.Server.LastMountHandshake(capCfg.ExportPath); !ok || !obs.Mounted || obs.ObservedUID != 0 || obs.ObservedGID != 0 || obs.AllowedUID != allowedUID || obs.AllowedGID != allowedGID {
		t.Fatalf("unexpected MountHandshakeObservation after MNT: ok=%v obs=%+v", ok, obs)
	}

	// 4. NFSPROC3_* procedures sent with UID=0, GID=0 must STILL be rejected with NFS3ERR_ACCES when AllowedUID != 0.
	rootNFS := dialRPC(t, env.Server.Addr(), 0, 0)
	if nfsSt, _ := rootNFS.getAttr(rootFH); nfsSt != nfs.NFSStatusAccess {
		t.Fatalf("expected NFSStatusAccess for NFSPROC3_GETATTR with UID=0,GID=0 when AllowedUID=%d, got %v", allowedUID, nfsSt)
	}
	if nfsSt, _, _ := rootNFS.lookup(rootFH, "README.md"); nfsSt != nfs.NFSStatusAccess {
		t.Fatalf("expected NFSStatusAccess for NFSPROC3_LOOKUP with UID=0,GID=0 when AllowedUID=%d, got %v", allowedUID, nfsSt)
	}

	// 5. XNU kernel NFSv3 data connection using the authorized (AllowedUID, AllowedGID) succeeds.
	userNFS := dialRPC(t, env.Server.Addr(), allowedUID, allowedGID)
	nfsSt, attr := userNFS.getAttr(rootFH)
	if nfsSt != nfs.NFSStatusOk || attr.Type != nfs.FileTypeDirectory {
		t.Fatalf("expected NFSStatusOk for authorized UID/GID GETATTR, got st=%v type=%v", nfsSt, attr.Type)
	}
	lookupSt, readmeFH, _ := userNFS.lookup(rootFH, "README.md")
	if lookupSt != nfs.NFSStatusOk {
		t.Fatalf("expected NFSStatusOk for authorized UID/GID LOOKUP, got %v", lookupSt)
	}
	readSt, data, _ := userNFS.read(readmeFH, 0, 64)
	if readSt != nfs.NFSStatusOk || string(data) != "hello world\n" {
		t.Fatalf("expected README.md content from authorized UID/GID READ, got st=%v data=%q", readSt, string(data))
	}

	// 6. Setuid-root userland umount helper (UID=0, GID=0) with valid capability token unmounts the export.
	rootUmnt := dialRPC(t, env.Server.Addr(), 0, 0)
	rootUmnt.umount(capCfg.ExportPath)
	if afterSt, _ := userNFS.getAttr(rootFH); afterSt != nfs.NFSStatusAccess {
		t.Fatalf("expected NFSStatusAccess after UID=0,GID=0 UMNT, got %v", afterSt)
	}
}
