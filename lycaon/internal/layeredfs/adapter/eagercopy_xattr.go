package adapter

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"syscall"

	"github.com/lycaon/lycaon/internal/layeredfs"
	"golang.org/x/sys/unix"
)

var (
	readPathXattrsHook  func(path string) error
	writePathXattrsHook func(path, name string, value []byte) error
	chmodPathHook       func(path string, mode fs.FileMode) error
)

func readPathXattrs(p string) (map[string][]byte, error) {
	if readPathXattrsHook != nil {
		if err := readPathXattrsHook(p); err != nil {
			return nil, err
		}
	}
	sz, err := unix.Llistxattr(p, nil)
	if err != nil {
		if isIgnorableXattrErr(err) {
			return nil, nil
		}
		return nil, err
	}
	if sz <= 0 {
		return nil, nil
	}
	buf := make([]byte, sz)
	n, err := unix.Llistxattr(p, buf)
	if err != nil {
		if isIgnorableXattrErr(err) {
			return nil, nil
		}
		return nil, err
	}
	if n <= 0 {
		return nil, nil
	}
	out := make(map[string][]byte)
	for _, rawName := range bytes.Split(buf[:n], []byte{0}) {
		if len(rawName) == 0 {
			continue
		}
		name := string(rawName)
		val, ok, gErr := readSinglePathXattr(p, name)
		if gErr != nil {
			return nil, gErr
		}
		if ok {
			out[name] = val
		}
	}
	if len(out) == 0 {
		return nil, nil
	}
	return out, nil
}

func readSinglePathXattr(p, name string) ([]byte, bool, error) {
	vsz, err := unix.Lgetxattr(p, name, nil)
	if err != nil {
		if isIgnorableXattrErr(err) {
			return nil, false, nil
		}
		return nil, false, err
	}
	if vsz < 0 {
		return nil, false, nil
	}
	if vsz == 0 {
		return []byte{}, true, nil
	}
	vbuf := make([]byte, vsz)
	vn, err := unix.Lgetxattr(p, name, vbuf)
	if err != nil {
		if isIgnorableXattrErr(err) {
			return nil, false, nil
		}
		return nil, false, err
	}
	if vn < 0 {
		return nil, false, nil
	}
	return vbuf[:vn], true, nil
}

func isIgnorableXattrErr(err error) bool {
	return errors.Is(err, unix.ENOTSUP) ||
		errors.Is(err, unix.EOPNOTSUPP) ||
		errors.Is(err, unix.ENOSYS) ||
		errors.Is(err, unix.ENODATA) ||
		errors.Is(err, syscall.Errno(93))
}

const (
	systemNFS4ACLXattr        = "system.nfs4_acl"
	systemPOSIXACLAccessXattr = "system.posix_acl_access"

	nfs4ACETypeAccessAllowed = 0
	nfs4ACETypeAccessDenied  = 1

	nfs4ACEReadData        = 0x00000001
	nfs4ACEWriteData       = 0x00000002
	nfs4ACEAppendData      = 0x00000004
	nfs4ACEExecute         = 0x00000020
	nfs4ACEDeleteChild     = 0x00000040
	nfs4ACEReadAttributes  = 0x00000080
	nfs4ACEWriteAttributes = 0x00000100
	nfs4ACEReadACL         = 0x00020000
	nfs4ACEWriteACL        = 0x00040000
	nfs4ACESynchronize     = 0x00100000

	// Linux fs/nfsd/nfs4acl.c mode bit translations:
	// - NFS4_READ_MODE    = NFS4_ACE_READ_DATA
	// - NFS4_WRITE_MODE   = NFS4_ACE_WRITE_DATA | NFS4_ACE_APPEND_DATA (+ NFS4_ACE_DELETE_CHILD on dirs)
	// - NFS4_EXECUTE_MODE = NFS4_ACE_EXECUTE
	// - NFS4_ANYONE_MODE  = NFS4_ACE_READ_ATTRIBUTES | NFS4_ACE_READ_ACL | NFS4_ACE_SYNCHRONIZE
	//   (unconditionally permitted to all principals in POSIX regardless of rwx mode bits)
	// - NFS4_OWNER_MODE   = NFS4_ACE_WRITE_ATTRIBUTES | NFS4_ACE_WRITE_ACL
	//   (unconditionally permitted to the inode owner in POSIX regardless of rwx mode bits)
	nfs4ReadMode    = nfs4ACEReadData
	nfs4WriteMode   = nfs4ACEWriteData | nfs4ACEAppendData
	nfs4ExecuteMode = nfs4ACEExecute
	nfs4AnyoneMode  = nfs4ACEReadAttributes | nfs4ACEReadACL | nfs4ACESynchronize
	nfs4OwnerMode   = nfs4ACEWriteAttributes | nfs4ACEWriteACL

	posixACLXattrVersion = 2
	posixACLTagUserObj   = 0x01
	posixACLTagGroupObj  = 0x04
	posixACLTagOther     = 0x20
	posixACLUndefinedID  = 0xffffffff
)

type nfs4CanonicalACE struct {
	aceType    uint32
	aceFlag    uint32
	accessMask uint32
	who        string
}

// normalizeSeedSourceXattrs distinguishes transport-synthesized mode-equivalent ACL representations
// (whose access semantics are already carried by mode.Perm() and mode.IsDir()) from non-trivial ACLs
// and portable extended attributes. All unproven ACLs fail closed during writePathXattrs.
func normalizeSeedSourceXattrs(mode fs.FileMode, xattrs map[string][]byte) map[string][]byte {
	if len(xattrs) == 0 {
		return nil
	}
	var out map[string][]byte
	for k, v := range xattrs {
		if isModeEquivalentTransportACL(k, v, mode) {
			continue
		}
		if out == nil {
			out = make(map[string][]byte, len(xattrs))
		}
		out[k] = v
	}
	return out
}

func isModeEquivalentTransportACL(name string, raw []byte, mode fs.FileMode) bool {
	if mode&fs.ModeType != 0 && mode&fs.ModeType != fs.ModeDir {
		return false
	}
	switch name {
	case systemNFS4ACLXattr:
		return isModeEquivalentNFS4ACL(raw, mode)
	case systemPOSIXACLAccessXattr:
		return isModeEquivalentPOSIXAccessACL(raw, mode)
	default:
		return false
	}
}

func nfs4DenyMaskFromPOSIX(perm fs.FileMode, isDir bool) uint32 {
	var mask uint32
	if perm&0o4 != 0 {
		mask |= nfs4ReadMode
	}
	if perm&0o2 != 0 {
		mask |= nfs4WriteMode
		if isDir {
			mask |= nfs4ACEDeleteChild
		}
	}
	if perm&0o1 != 0 {
		mask |= nfs4ExecuteMode
	}
	return mask
}

func nfs4AllowMaskFromPOSIX(perm fs.FileMode, isDir, isOwner bool) uint32 {
	mask := uint32(nfs4AnyoneMode)
	if isOwner {
		mask |= nfs4OwnerMode
	}
	return mask | nfs4DenyMaskFromPOSIX(perm, isDir)
}

// canonicalLinuxNFS4ModeACL computes the exact NFSv4 ACL sequence synthesized by Linux knfsd
// (fs/nfsd/nfs4acl.c: nfsd4_get_nfs4_acl + _posix_to_nfsv4_one) for a mode-only inode.
func canonicalLinuxNFS4ModeACL(mode fs.FileMode) ([5]nfs4CanonicalACE, int) {
	perm := mode.Perm()
	owner := (perm >> 6) & 0o7
	group := (perm >> 3) & 0o7
	other := perm & 0o7
	isDir := mode.IsDir()

	var aces [5]nfs4CanonicalACE
	n := 0
	if ownerDeny := ((group | other) &^ owner) & 0o7; ownerDeny != 0 {
		aces[n] = nfs4CanonicalACE{
			aceType:    nfs4ACETypeAccessDenied,
			aceFlag:    0,
			accessMask: nfs4DenyMaskFromPOSIX(ownerDeny, isDir),
			who:        "OWNER@",
		}
		n++
	}
	aces[n] = nfs4CanonicalACE{
		aceType:    nfs4ACETypeAccessAllowed,
		aceFlag:    0,
		accessMask: nfs4AllowMaskFromPOSIX(owner, isDir, true),
		who:        "OWNER@",
	}
	n++
	aces[n] = nfs4CanonicalACE{
		aceType:    nfs4ACETypeAccessAllowed,
		aceFlag:    0,
		accessMask: nfs4AllowMaskFromPOSIX(group, isDir, false),
		who:        "GROUP@",
	}
	n++
	if groupDeny := (other &^ group) & 0o7; groupDeny != 0 {
		aces[n] = nfs4CanonicalACE{
			aceType:    nfs4ACETypeAccessDenied,
			aceFlag:    0,
			accessMask: nfs4DenyMaskFromPOSIX(groupDeny, isDir),
			who:        "GROUP@",
		}
		n++
	}
	aces[n] = nfs4CanonicalACE{
		aceType:    nfs4ACETypeAccessAllowed,
		aceFlag:    0,
		accessMask: nfs4AllowMaskFromPOSIX(other, isDir, false),
		who:        "EVERYONE@",
	}
	n++
	return aces, n
}

func isModeEquivalentNFS4ACL(raw []byte, mode fs.FileMode) bool {
	if mode&fs.ModeType != 0 && mode&fs.ModeType != fs.ModeDir {
		return false
	}
	if len(raw) < 4 {
		return false
	}
	expected, expectedCount := canonicalLinuxNFS4ModeACL(mode)
	count := binary.BigEndian.Uint32(raw[:4])
	if count != uint32(expectedCount) {
		return false
	}
	off := 4
	for i := 0; i < expectedCount; i++ {
		if off+16 > len(raw) {
			return false
		}
		aceType := binary.BigEndian.Uint32(raw[off : off+4])
		aceFlag := binary.BigEndian.Uint32(raw[off+4 : off+8])
		accessMask := binary.BigEndian.Uint32(raw[off+8 : off+12])
		whoLen := int(binary.BigEndian.Uint32(raw[off+12 : off+16]))
		off += 16
		if whoLen <= 0 || whoLen > len(raw)-off {
			return false
		}
		padded := (whoLen + 3) &^ 3
		if padded > len(raw)-off {
			return false
		}
		who := string(raw[off : off+whoLen])
		for padIdx := off + whoLen; padIdx < off+padded; padIdx++ {
			if raw[padIdx] != 0 {
				return false
			}
		}
		off += padded
		want := expected[i]
		if aceType != want.aceType || aceFlag != want.aceFlag || accessMask != want.accessMask || who != want.who {
			return false
		}
	}
	return off == len(raw)
}

func isModeEquivalentPOSIXAccessACL(raw []byte, mode fs.FileMode) bool {
	if mode&fs.ModeType != 0 && mode&fs.ModeType != fs.ModeDir {
		return false
	}
	// Linux posix_acl_xattr_header (4-byte version = 2) + 3 * posix_acl_xattr_entry (8 bytes each) = 28 bytes
	// in canonical posix_acl_valid / posix_acl_from_mode order (fs/posix_acl.c).
	if len(raw) != 28 {
		return false
	}
	if binary.LittleEndian.Uint32(raw[0:4]) != posixACLXattrVersion {
		return false
	}
	perm := mode.Perm()
	expected := [3]struct {
		tag  uint16
		perm uint16
	}{
		{tag: posixACLTagUserObj, perm: uint16((perm >> 6) & 0o7)},
		{tag: posixACLTagGroupObj, perm: uint16((perm >> 3) & 0o7)},
		{tag: posixACLTagOther, perm: uint16(perm & 0o7)},
	}
	for i, want := range expected {
		off := 4 + i*8
		tag := binary.LittleEndian.Uint16(raw[off : off+2])
		ePerm := binary.LittleEndian.Uint16(raw[off+2 : off+4])
		id := binary.LittleEndian.Uint32(raw[off+4 : off+8])
		if tag != want.tag || ePerm != want.perm || id != posixACLUndefinedID {
			return false
		}
	}
	return true
}

func writePathXattrs(p string, xattrs map[string][]byte) error {
	for k, v := range xattrs {
		if writePathXattrsHook != nil {
			if err := writePathXattrsHook(p, k, v); err != nil {
				return fmt.Errorf("write xattr %q on %q: %w", k, p, err)
			}
		}
		if err := unix.Lsetxattr(p, k, v, 0); err != nil {
			return fmt.Errorf("write xattr %q on %q: %w", k, p, err)
		}
	}
	return nil
}

func chmodPath(p string, mode fs.FileMode) error {
	if chmodPathHook != nil {
		if err := chmodPathHook(p, mode); err != nil {
			return fmt.Errorf("chmod %q to %v: %w", p, mode, err)
		}
	}
	if err := os.Chmod(p, mode); err != nil {
		return fmt.Errorf("chmod %q to %v: %w", p, mode, err)
	}
	return nil
}

func cloneBaseCaptureRecord(b layeredfs.BaseCapture) layeredfs.BaseCapture {
	b.Xattrs = cloneXattrMap(b.Xattrs)
	return b
}

func cloneXattrMap(in map[string][]byte) map[string][]byte {
	if len(in) == 0 {
		return nil
	}
	out := make(map[string][]byte, len(in))
	for k, v := range in {
		out[k] = append([]byte(nil), v...)
	}
	return out
}

func xattrsEqual(a, b map[string][]byte) bool {
	if len(a) != len(b) {
		return false
	}
	for k, va := range a {
		vb, ok := b[k]
		if !ok || !bytes.Equal(va, vb) {
			return false
		}
	}
	return true
}

func applyXattrDiff(core *layeredfs.Core, qp layeredfs.QualifiedPath, diskXattrs map[string][]byte) error {
	existingNames, err := core.ListXattr(qp, true)
	if err != nil {
		return err
	}
	existingSet := make(map[string]struct{}, len(existingNames))
	for _, name := range existingNames {
		existingSet[name] = struct{}{}
		if _, keep := diskXattrs[name]; !keep {
			if err := core.RemoveXattr(qp, name, true); err != nil {
				return err
			}
		}
	}
	for name, val := range diskXattrs {
		if _, had := existingSet[name]; had {
			oldVal, gErr := core.GetXattr(qp, name, true)
			if gErr == nil && bytes.Equal(oldVal, val) {
				continue
			}
		}
		if err := core.SetXattr(qp, name, val, true); err != nil {
			return err
		}
	}
	return nil
}

func entryKindMatchesMode(kind layeredfs.EntryKind, mode fs.FileMode) bool {
	switch kind {
	case layeredfs.EntryDirectory:
		return mode.IsDir()
	case layeredfs.EntrySymlink:
		return mode&os.ModeSymlink != 0
	case layeredfs.EntryRegular:
		return mode.IsRegular()
	default:
		return false
	}
}

func makeDirsWritable(root string) {
	if root == "" {
		return
	}
	_ = filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			if info, iErr := d.Info(); iErr == nil && info.Mode().Perm()&0o700 != 0o700 {
				_ = os.Chmod(p, info.Mode().Perm()|0o700)
			}
		}
		return nil
	})
}

func removeAllForceWritable(root string) error {
	if root == "" {
		return nil
	}
	makeDirsWritable(root)
	return os.RemoveAll(root)
}
