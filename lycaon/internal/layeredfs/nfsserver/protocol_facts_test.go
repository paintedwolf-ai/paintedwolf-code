package nfsserver_test

import (
	"bytes"
	"io"
	"net"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/lycaon/lycaon/internal/layeredfs"
	"github.com/lycaon/lycaon/internal/layeredfs/nfsserver"
	nfs "github.com/willscott/go-nfs"
)

func TestListingsAreFreshAndCookiesBelongToOnePass(t *testing.T) {
	env := newLimitedEnv(t, nfsserver.Config{}, nil, seedFiles(t, 60))
	cli := env.cli
	var zero [8]byte
	st, l1 := cli.readdir(env.root, 0, zero, 512)
	if st != nfs.NFSStatusOk || l1.eof {
		t.Fatalf("first page: %v eof=%v", st, l1.eof)
	}

	// A trunk file added after the listing started: an in-progress pass stays self-consistent, the
	// next pass sees it.
	if err := os.WriteFile(filepath.Join(env.trunk, "added-in-trunk"), []byte("t"), 0o644); err != nil {
		t.Fatalf("trunk add: %v", err)
	}
	st, l2 := cli.readdir(env.root, l1.cookie, l1.verf, 512)
	if st != nfs.NFSStatusOk {
		t.Fatalf("second page: %v", st)
	}
	for _, n := range l2.names {
		if n == "added-in-trunk" {
			t.Fatal("a listing pass changed under its cookies")
		}
	}
	found := false
	all := map[string]uint64{}
	for cookie, verf, eof := uint64(0), zero, false; !eof; {
		st, page := cli.readdir(env.root, cookie, verf, 4096)
		if st != nfs.NFSStatusOk {
			t.Fatalf("fresh pass: %v", st)
		}
		for n, id := range page.ids {
			if _, dup := all[n]; dup {
				t.Fatalf("%s listed twice", n)
			}
			all[n] = id
			found = found || n == "added-in-trunk"
		}
		cookie, verf, eof = page.cookie, page.verf, page.eof
	}
	if !found || len(all) != 63 {
		t.Fatalf("fresh pass saw %d entries, trunk add found=%v", len(all), found)
	}

	// Mutation through the server ends the pass; the old cookie is refused, never silently short.
	st, l3 := cli.readdir(env.root, 0, zero, 512)
	if st != nfs.NFSStatusOk {
		t.Fatalf("restart: %v", st)
	}
	if st, _ := cli.create(env.root, "new-through-server", 0o644, 0, [8]byte{}); st != nfs.NFSStatusOk {
		t.Fatalf("create: %v", st)
	}
	if st, _ := cli.readdir(env.root, l3.cookie, l3.verf, 512); st != nfs.NFSStatusBadCookie {
		t.Fatalf("cookie after mutation: %v", st)
	}

	// fileid agrees between LOOKUP, GETATTR and READDIR across copy-up.
	st, fh, before := cli.lookup(env.root, "f0003")
	if st != nfs.NFSStatusOk {
		t.Fatalf("lookup: %v", st)
	}
	if st, _, _, _ := cli.write(fh, 0, []byte("copied-up"), nfsserver.FileSync); st != nfs.NFSStatusOk {
		t.Fatalf("write: %v", st)
	}
	if st, after := cli.getAttr(fh); st != nfs.NFSStatusOk || after.Fileid != before.Fileid {
		t.Fatalf("fileid moved across copy-up: %d -> %d", before.Fileid, after.Fileid)
	}
	all = map[string]uint64{}
	for cookie, verf, eof := uint64(0), zero, false; !eof; {
		st, page := cli.readdir(env.root, cookie, verf, 4096)
		if st != nfs.NFSStatusOk {
			t.Fatalf("pass after copy-up: %v", st)
		}
		for n, id := range page.ids {
			all[n] = id
		}
		cookie, verf, eof = page.cookie, page.verf, page.eof
	}
	if all["f0003"] != before.Fileid {
		t.Fatalf("READDIR fileid %d differs from GETATTR %d", all["f0003"], before.Fileid)
	}
}

func TestExportAdmitsOneMountAndUmountNeverRevokes(t *testing.T) {
	env := newLimitedEnv(t, nfsserver.Config{}, nil, seedFiles(t, 1))
	other := dialRPC(t, env.srv.Addr(), uint32(os.Geteuid()), uint32(os.Getegid()))
	if st, fh := other.mount(env.cap.ExportPath); st != nfs.MountStatusErrAcces || fh != nil {
		t.Fatalf("second mount of a spent capability: %v", st)
	}
	var a bytes.Buffer
	putStr(&a, env.cap.ExportPath)
	third := dialRPC(t, env.srv.Addr(), uint32(os.Geteuid()), uint32(os.Getegid()))
	if code, _ := third.call(100005, 3, uint32(nfs.MountProcUmnt), a.Bytes()); code != nfs.ResponseCodeSuccess {
		t.Fatalf("UMNT accept: %v", code)
	}
	if st, _ := env.cli.getAttr(env.root); st != nfs.NFSStatusOk {
		t.Fatalf("UMNT from another client revoked the mount: %v", st)
	}
	if st, fh := third.mount(env.cap.ExportPath); st != nfs.MountStatusErrAcces || fh != nil {
		t.Fatalf("mount after UMNT: %v", st)
	}
}

// TestWritesFollowTheDescriptorNotTheCurrentMode replays what the macOS client sends for
// open(O_CREAT|O_EXCL, 0444): an EXCLUSIVE create, then SETATTR, then writes through the handle.
// Git loose objects depend on it, and a descriptor keeps write access through a later chmod.
func TestWritesFollowTheDescriptorNotTheCurrentMode(t *testing.T) {
	env := newLimitedEnv(t, nfsserver.Config{}, nil, nil)
	cli := env.cli
	st, fh := cli.create(env.root, "obj", 0, 2, [8]byte{1, 2, 3})
	if st != nfs.NFSStatusOk {
		t.Fatalf("exclusive create: %v", st)
	}
	mode := uint32(0o444)
	if st := cli.setattr(fh, &mode, nil); st != nfs.NFSStatusOk {
		t.Fatalf("setattr 0444: %v", st)
	}
	if st, _, _, _ := cli.write(fh, 0, []byte("object"), nfsserver.Unstable); st != nfs.NFSStatusOk {
		t.Fatalf("write 0444: %v", st)
	}
	if st, _ := cli.commit(fh, 0, 6); st != nfs.NFSStatusOk {
		t.Fatalf("commit 0444: %v", st)
	}
	data, err := env.core.ReadFile(layeredfs.QualifiedPath{RootID: nfsserver.ProjectRoot, Rel: "obj"})
	if err != nil || string(data) != "object" {
		t.Fatalf("object bytes: %q err=%v", data, err)
	}
	// ACCESS reports the mode, so a new open for write is refused by the client like a native one.
	var a bytes.Buffer
	putBytes(&a, fh)
	putU32(&a, 0x0004|0x0008)
	code, r := cli.call(procNFS, verNFS, uint32(nfs.NFSProcedureAccess), a.Bytes())
	if code != nfs.ResponseCodeSuccess || nfs.NFSStatus(getU32(t, r)) != nfs.NFSStatusOk {
		t.Fatal("ACCESS failed")
	}
	if getU32(t, r) == 1 {
		discardFattr(t, r)
	}
	if granted := getU32(t, r); granted != 0 {
		t.Fatalf("ACCESS granted write %#x on a 0444 file", granted)
	}
}

func TestMknodCreatesFIFOsAndSocketsOnly(t *testing.T) {
	env := newLimitedEnv(t, nfsserver.Config{}, nil, nil)
	cli := env.cli
	mknod := func(name string, ftype nfs.FileType, extra func(*bytes.Buffer)) (nfs.NFSStatus, []byte) {
		t.Helper()
		var a bytes.Buffer
		putBytes(&a, env.root)
		putStr(&a, name)
		putU32(&a, uint32(ftype))
		extra(&a)
		code, r := cli.call(procNFS, verNFS, uint32(nfs.NFSProcedureMkNod), a.Bytes())
		if code != nfs.ResponseCodeSuccess {
			t.Fatalf("MKNOD accept: %v", code)
		}
		st := nfs.NFSStatus(getU32(t, r))
		if st != nfs.NFSStatusOk {
			return st, nil
		}
		getU32(t, r)
		return st, getBytes(t, r)
	}
	withMode := func(a *bytes.Buffer) { encodeSattrMode(a, true, 0o640) }
	st, fifo := mknod("pipe", nfs.FileTypeFIFO, withMode)
	if st != nfs.NFSStatusOk {
		t.Fatalf("fifo: %v", st)
	}
	if st, attr := cli.getAttr(fifo); st != nfs.NFSStatusOk || attr.Type != nfs.FileTypeFIFO || attr.FileMode&0o777 != 0o640 {
		t.Fatalf("fifo attrs: st=%v type=%v mode=%o", st, attr.Type, attr.FileMode)
	}
	st, sock := mknod("sock", nfs.FileTypeSocket, withMode)
	if st != nfs.NFSStatusOk {
		t.Fatalf("socket: %v", st)
	}
	if st, attr := cli.getAttr(sock); st != nfs.NFSStatusOk || attr.Type != nfs.FileTypeSocket {
		t.Fatalf("socket attrs: st=%v type=%v", st, attr.Type)
	}
	if st, _ := mknod("dev", nfs.FileTypeCharacter, func(a *bytes.Buffer) { encodeSattrMode(a, true, 0o600); putU32(a, 1); putU32(a, 2) }); st != nfs.NFSStatusPerm {
		t.Fatalf("device: %v", st)
	}
	if st, _ := mknod("reg", nfs.FileTypeRegular, func(*bytes.Buffer) {}); uint32(st) != 10007 {
		t.Fatalf("regular through MKNOD: %v", st)
	}
	if st, _ := mknod("pipe", nfs.FileTypeFIFO, withMode); st != nfs.NFSStatusExist {
		t.Fatalf("existing name: %v", st)
	}
	changes, err := env.core.EnumerateChanges(true)
	if err != nil || len(changes) != 0 {
		t.Fatalf("special nodes in changes: %+v err=%v", changes, err)
	}
}

func TestFilesystemFactsAreReal(t *testing.T) {
	env := newLimitedEnv(t, nfsserver.Config{}, nil, nil)
	capacity, err := env.core.BackingCapacity()
	if err != nil || capacity.TotalBytes == 0 {
		t.Fatalf("capacity: %+v err=%v", capacity, err)
	}
	var a bytes.Buffer
	putBytes(&a, env.root)
	code, r := env.cli.call(procNFS, verNFS, uint32(nfs.NFSProcedureFSStat), a.Bytes())
	if code != nfs.ResponseCodeSuccess || nfs.NFSStatus(getU32(t, r)) != nfs.NFSStatusOk {
		t.Fatal("FSSTAT failed")
	}
	if getU32(t, r) == 1 {
		discardFattr(t, r)
	}
	if total := getU64(t, r); total != capacity.TotalBytes {
		t.Fatalf("FSSTAT total %d, backing has %d", total, capacity.TotalBytes)
	}
	code, r = env.cli.call(procNFS, verNFS, uint32(nfs.NFSProcedurePathConf), a.Bytes())
	if code != nfs.ResponseCodeSuccess || nfs.NFSStatus(getU32(t, r)) != nfs.NFSStatusOk {
		t.Fatal("PATHCONF failed")
	}
	if getU32(t, r) == 1 {
		discardFattr(t, r)
	}
	if linkMax := getU32(t, r); linkMax < 2 {
		t.Fatalf("PATHCONF linkmax %d denies hard links the server supports", linkMax)
	}
	code, r = env.cli.call(procNFS, verNFS, uint32(nfs.NFSProcedureFSInfo), a.Bytes())
	if code != nfs.ResponseCodeSuccess || nfs.NFSStatus(getU32(t, r)) != nfs.NFSStatusOk {
		t.Fatal("FSINFO failed")
	}
	if getU32(t, r) == 1 {
		discardFattr(t, r)
	}
	if _, err := r.Seek(7*4+8+8, io.SeekCurrent); err != nil {
		t.Fatalf("skip FSINFO: %v", err)
	}
	if props := getU32(t, r); props&0x0001 == 0 {
		t.Fatalf("FSINFO properties %#x omit hard link support", props)
	}
}

// TestCloseUnderTrafficSettles closes the server while connections send requests: no request may
// reach a closed channel, and Close must return.
func TestCloseUnderTrafficSettles(t *testing.T) {
	for round := range 30 {
		env := newLimitedEnv(t, nfsserver.Config{}, nil, seedFiles(t, 1))
		var wg sync.WaitGroup
		for range 4 {
			conn, err := net.Dial("tcp", env.srv.Addr().String())
			if err != nil {
				t.Fatalf("dial: %v", err)
			}
			wg.Add(1)
			go func() {
				defer wg.Done()
				defer func() { _ = conn.Close() }()
				var msg bytes.Buffer
				for _, v := range []uint32{1, 0, 2, 100003, 3, 0, 0, 0, 0, 0} {
					putU32(&msg, v)
				}
				var frame bytes.Buffer
				putU32(&frame, uint32(msg.Len())|0x80000000)
				_, _ = frame.Write(msg.Bytes())
				for {
					if _, err := conn.Write(frame.Bytes()); err != nil {
						return
					}
				}
			}()
		}
		if err := env.srv.Close(); err != nil {
			t.Fatalf("round %d close: %v", round, err)
		}
		wg.Wait()
	}
}

func TestAppleDoubleMatchesTheKernelLayout(t *testing.T) {
	// A ._ file written by the macOS kernel holding two attributes.
	real := make([]byte, 4096)
	copy(real, []byte{0x00, 0x05, 0x16, 0x07, 0x00, 0x02, 0x00, 0x00})
	copy(real[8:], "Mac OS X        ")
	copy(real[24:], []byte{0, 2, 0, 0, 0, 9, 0, 0, 0, 0x32, 0, 0, 0x0e, 0xb0, 0, 0, 0, 2, 0, 0, 0x0e, 0xe2, 0, 0, 1, 0x1e})
	copy(real[0x54:], []byte{0x41, 0x54, 0x54, 0x52, 0, 0, 0, 0, 0, 0, 0x0e, 0xe2, 0, 0, 0, 0xb0, 0, 0, 0, 0x10})
	copy(real[0x76:], []byte{0, 2})
	copy(real[0x78:], []byte{0, 0, 0, 0xb0, 0, 0, 0, 0x0b, 0, 0, 0x15})
	copy(real[0x83:], "com.apple.provenance\x00")
	copy(real[0x98:], []byte{0, 0, 0, 0xbb, 0, 0, 0, 5, 0, 0, 0x0b})
	copy(real[0xa3:], "user.alpha\x00")
	copy(real[0xb0:], []byte{1, 2, 0, 0x43, 0xfd, 0xd6, 0x92, 0xd4, 0x2c, 0x41, 0x87})
	copy(real[0xbb:], "bravo")
	kernelBlankFork(real[0xee2:])
	_, attrs, ok := nfsserver.DecodeAppleDoubleXattrs(real)
	if !ok || string(attrs["user.alpha"]) != "bravo" || len(attrs["com.apple.provenance"]) != 11 || len(attrs) != 2 {
		t.Fatalf("kernel layout decoded as %v ok=%v", attrs, ok)
	}

	encoded := nfsserver.EncodeAppleDoubleXattrs([32]byte{}, attrs)
	if len(encoded) != 4096 {
		t.Fatalf("encoded size %d", len(encoded))
	}
	if !bytes.Equal(encoded[:0x1a], real[:0x1a]) || !bytes.Equal(encoded[0x54:0x58], real[0x54:0x58]) {
		t.Fatalf("header differs from the kernel's:\n%x\n%x", encoded[:0x54], real[:0x54])
	}
	_, back, ok := nfsserver.DecodeAppleDoubleXattrs(encoded)
	if !ok || len(back) != 2 || string(back["user.alpha"]) != "bravo" {
		t.Fatalf("round trip: %v ok=%v", back, ok)
	}
	big := map[string][]byte{"user.big": bytes.Repeat([]byte("v"), 9000), "com.apple.FinderInfo": bytes.Repeat([]byte{7}, 32)}
	_, back, ok = nfsserver.DecodeAppleDoubleXattrs(nfsserver.EncodeAppleDoubleXattrs([32]byte{}, big))
	if !ok || len(back["user.big"]) != 9000 || len(back["com.apple.FinderInfo"]) != 32 {
		t.Fatalf("large attribute round trip: %d ok=%v", len(back), ok)
	}
	for cut := 0; cut < 200; cut += 7 {
		_, _, _ = nfsserver.DecodeAppleDoubleXattrs(encoded[:cut])
	}

	// A fork with content is an attribute, and a table no field accounts for is not this layout.
	forked := bytes.Clone(real)
	forked[len(forked)-1] ^= 0xff
	if _, got, ok := nfsserver.DecodeAppleDoubleXattrs(forked); !ok || len(got["com.apple.ResourceFork"]) != 286 {
		t.Fatalf("fork with content: %d bytes ok=%v", len(got["com.apple.ResourceFork"]), ok)
	}
	reserved := bytes.Clone(real)
	reserved[0x6c] = 1
	if _, _, ok := nfsserver.DecodeAppleDoubleXattrs(reserved); ok {
		t.Fatal("a set reserved word decoded as complete")
	}
	beforeData := bytes.Clone(real)
	copy(beforeData[0x63:], []byte{0xc0}) // data_start past the table leaves bytes no field accounts for
	if _, _, ok := nfsserver.DecodeAppleDoubleXattrs(beforeData); ok {
		t.Fatal("a value outside the data area decoded as complete")
	}
	// Free space past the data area is the kernel's own leftovers, and holds no attribute.
	free := bytes.Clone(real)
	free[0x200] = 1
	if _, got, ok := nfsserver.DecodeAppleDoubleXattrs(free); !ok || len(got) != 2 {
		t.Fatalf("free space: %d attributes ok=%v", len(got), ok)
	}
}

// kernelBlankFork writes the 286-byte placeholder fork observed in kernel-written ._ files.
func kernelBlankFork(fork []byte) {
	for _, at := range []int{0, 256} {
		copy(fork[at:], []byte{0, 0, 1, 0, 0, 0, 1, 0, 0, 0, 0, 0, 0, 0, 0, 0x1e})
	}
	copy(fork[16:], "This resource fork intentionally left blank   ")
	copy(fork[280:], []byte{0, 0x1c, 0, 0x1e, 0xff, 0xff})
}
