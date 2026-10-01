package lineage

import (
	"encoding/binary"
	"net/netip"
	"testing"
)

// The kernel prints each 32-bit address word in host byte order; these
// vectors are what a little-endian host writes.
func TestParseProcAddrPortDecodesBothFamilies(t *testing.T) {
	if binary.NativeEndian.Uint16([]byte{1, 0}) != 1 {
		t.Skip("vectors are little-endian procfs output")
	}
	for field, want := range map[string]string{
		"0100007F:1F90":                         "127.0.0.1:8080",
		"00000000:0BB8":                         "0.0.0.0:3000",
		"00000000000000000000000000000000:0BB8": "[::]:3000",
		"00000000000000000000000001000000:0BB8": "[::1]:3000",
		"0000000000000000FFFF00000100007F:0BB8": "[::ffff:127.0.0.1]:3000",
		"0000000000000000FFFF000000000000:0050": "[::ffff:0.0.0.0]:80",
	} {
		got, ok := parseProcAddrPort(field)
		if !ok || got != netip.MustParseAddrPort(want) {
			t.Errorf("parseProcAddrPort(%q) = %v, %v; want %s", field, got, ok, want)
		}
	}
	for _, field := range []string{"", "0100007F", "0100007:1F90", "0100007F:XYZ", "0100007F0:1F90"} {
		if got, ok := parseProcAddrPort(field); ok {
			t.Errorf("parseProcAddrPort(%q) = %v, want rejection", field, got)
		}
	}
}

func TestParseProcNetTCPReadsStateAndInode(t *testing.T) {
	if binary.NativeEndian.Uint16([]byte{1, 0}) != 1 {
		t.Skip("vectors are little-endian procfs output")
	}
	table := []byte(`  sl  local_address rem_address   st tx_queue rx_queue tr tm->when retrnsmt   uid  timeout inode
   0: 0100007F:1F90 00000000:0000 0A 00000000:00000000 00:00000000 00000000  1000        0 41234 1 0000000000000000 100 0 0 10 0
   1: 0100007F:C350 0100007F:1F90 01 00000000:00000000 00:00000000 00000000  1000        0 41299 1 0000000000000000 20 4 30 10 -1
`)
	rows := parseProcNetTCP(table)
	if len(rows) != 2 {
		t.Fatalf("rows = %d, want 2: %+v", len(rows), rows)
	}
	if !rows[0].Listening || rows[0].Inode != 41234 || rows[0].Local != netip.MustParseAddrPort("127.0.0.1:8080") {
		t.Fatalf("listener row = %+v", rows[0])
	}
	if rows[1].Listening || rows[1].Remote != netip.MustParseAddrPort("127.0.0.1:8080") {
		t.Fatalf("connection row = %+v", rows[1])
	}
}
