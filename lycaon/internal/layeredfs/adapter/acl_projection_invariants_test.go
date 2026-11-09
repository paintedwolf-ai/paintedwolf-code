package adapter

import (
	"bytes"
	"encoding/hex"
	"io/fs"
	"testing"
)

// Captured with chmod/getxattr on a Linux kernel NFS4.2 export, independently of
// the classifier. The file includes a group denial; the directory grants delete-child.
func TestSeedNormalizationRecognizesOnlyProvenTransportProjection(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mode   fs.FileMode
		hexACL string
	}{
		{"file", 0o404, "00000004000000000000000000160181000000064f574e45524000000000000000000000001200800000000647524f55504000000000000100000000000000010000000647524f55504000000000000000000000001200810000000945564552594f4e4540000000"},
		{"directory", fs.ModeDir | 0o755, "000000030000000000000000001601e7000000064f574e45524000000000000000000000001200a10000000647524f55504000000000000000000000001200a10000000945564552594f4e4540000000"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			raw, err := hex.DecodeString(tc.hexACL)
			if err != nil {
				t.Fatalf("decode independent kernel fixture: %v", err)
			}
			if got := normalizeSeedSourceXattrs(tc.mode, map[string][]byte{"system.nfs4_acl": raw}); len(got) != 0 {
				t.Fatal("canonical transport ACL was not recognized")
			}
			// Every single-bit deviation from the proven projection must survive intact.
			for bit := 0; bit < len(raw)*8; bit++ {
				changed := bytes.Clone(raw)
				changed[bit/8] ^= 1 << (bit % 8)
				expected := bytes.Clone(changed)
				got := normalizeSeedSourceXattrs(tc.mode, map[string][]byte{"system.nfs4_acl": changed})
				if !bytes.Equal(got["system.nfs4_acl"], expected) || !bytes.Equal(changed, expected) {
					t.Fatalf("unproven ACL deviation at bit %d lost metadata", bit)
				}
			}
		})
	}
}
