package adapter

import (
	"bytes"
	"encoding/binary"
	"io/fs"
	"testing"
)

// RFC 7530 section 6.2.1.3 assigns READ_DATA=1, WRITE_DATA=2 and APPEND_DATA=4.
// Recognized principals alone do not make their ordered permissions equal to a mode.
func TestSeedNormalizationPreservesACLsThatDifferFromMode(t *testing.T) {
	for _, tc := range []struct {
		name              string
		mode              fs.FileMode
		kind, flags, mask uint32
	}{
		{"read grant absent from mode", 0, 0, 0, 1},
		{"read denial contradicts mode", 0o777, 1, 0, 1},
		{"world write exceeds owner read", 0o400, 0, 0, 7},
		{"unknown flags cannot establish equivalence", 0o444, 0, 0x80000000, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			raw := encodeACLPermissionFixture(tc.kind, tc.flags, tc.mask, "EVERYONE@")
			input := map[string][]byte{"system.nfs4_acl": raw, "user.content": []byte("retain")}
			got := normalizeSeedSourceXattrs(tc.mode, input)
			if !bytes.Equal(got["system.nfs4_acl"], raw) || !bytes.Equal(got["user.content"], []byte("retain")) {
				t.Fatal("normalization discarded ACL authority or portable metadata")
			}
			if !bytes.Equal(input["system.nfs4_acl"], raw) {
				t.Fatal("normalization mutated source metadata")
			}
		})
	}
}

func encodeACLPermissionFixture(kind, flags, mask uint32, who string) []byte {
	raw := binary.BigEndian.AppendUint32(nil, 1)
	for _, value := range []uint32{kind, flags, mask, uint32(len(who))} {
		raw = binary.BigEndian.AppendUint32(raw, value)
	}
	raw = append(raw, []byte(who)...)
	for len(raw)%4 != 0 {
		raw = append(raw, 0)
	}
	return raw
}

func TestSeedNormalizationRejectsUnsupportedPOSIXPermissionBits(t *testing.T) {
	raw := binary.LittleEndian.AppendUint32(nil, 2)
	for _, entry := range []struct{ tag, permission uint16 }{{1, 0x8006}, {4, 0}, {32, 0}} {
		raw = binary.LittleEndian.AppendUint16(raw, entry.tag)
		raw = binary.LittleEndian.AppendUint16(raw, entry.permission)
		raw = binary.LittleEndian.AppendUint32(raw, 0xffffffff)
	}
	got := normalizeSeedSourceXattrs(0o600, map[string][]byte{"system.posix_acl_access": raw})
	if !bytes.Equal(got["system.posix_acl_access"], raw) {
		t.Fatal("unsupported permission bits were silently masked into mode equivalence")
	}
}
