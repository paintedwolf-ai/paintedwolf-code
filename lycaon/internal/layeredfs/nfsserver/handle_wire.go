package nfsserver

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/binary"

	"github.com/lycaon/lycaon/internal/layeredfs"
)

const (
	handleMagic   uint32 = 0x4C594653 // "LYFS"
	handleVersion uint8  = 1
	rootSlotProj  uint8  = 1
	rootSlotWork  uint8  = 2
)

func encodeWireHandle(secret [32]byte, vHash uint64, root RootID, nodeID, parentID layeredfs.NodeID, gen uint64) [HandleSize]byte {
	var out [HandleSize]byte
	binary.BigEndian.PutUint32(out[0:4], handleMagic)
	out[4], out[5] = handleVersion, rootSlotProj
	if root == WorktreeRoot {
		out[5] = rootSlotWork
	}
	binary.BigEndian.PutUint64(out[8:16], vHash)
	binary.BigEndian.PutUint64(out[16:24], uint64(nodeID))
	binary.BigEndian.PutUint64(out[24:32], gen)
	binary.BigEndian.PutUint64(out[32:40], uint64(parentID))
	mac := hmac.New(sha256.New, secret[:])
	_, _ = mac.Write(out[0:40])
	copy(out[40:64], mac.Sum(nil)[:24])
	return out
}

func decodeWireHandle(secret [32]byte, raw []byte) (wireHeader, error) {
	if len(raw) != HandleSize || binary.BigEndian.Uint32(raw[0:4]) != handleMagic || raw[4] != handleVersion {
		return wireHeader{}, ErrForgedHandle
	}
	var root RootID
	switch raw[5] {
	case rootSlotProj:
		root = ProjectRoot
	case rootSlotWork:
		root = WorktreeRoot
	default:
		return wireHeader{}, ErrForgedHandle
	}
	mac := hmac.New(sha256.New, secret[:])
	_, _ = mac.Write(raw[0:40])
	if !hmac.Equal(raw[40:64], mac.Sum(nil)[:24]) {
		return wireHeader{}, ErrForgedHandle
	}
	return wireHeader{
		root: root, viewHash: binary.BigEndian.Uint64(raw[8:16]),
		nodeID:     layeredfs.NodeID(binary.BigEndian.Uint64(raw[16:24])),
		generation: binary.BigEndian.Uint64(raw[24:32]),
		parentID:   layeredfs.NodeID(binary.BigEndian.Uint64(raw[32:40])),
	}, nil
}
