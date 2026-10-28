package lineage

import (
	"encoding/binary"
	"fmt"
	"net/netip"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
)

// procAddr writes an endpoint the way this host's kernel prints it: 32-bit
// address words in host byte order, then the port in big-endian hex.
func procAddr(t *testing.T, endpoint string) string {
	t.Helper()
	ap := netip.MustParseAddrPort(endpoint)
	raw := ap.Addr().AsSlice()
	var b strings.Builder
	for i := 0; i < len(raw); i += 4 {
		fmt.Fprintf(&b, "%08X", binary.NativeEndian.Uint32(raw[i:i+4]))
	}
	return fmt.Sprintf("%s:%04X", b.String(), ap.Port())
}

type procRow struct {
	local, remote, state string
	uid                  uint32
	inode                uint64
}

func writeProcTable(t *testing.T, root, name string, rows ...procRow) {
	t.Helper()
	lines := []string{"  sl  local_address rem_address   st tx_queue rx_queue tr tm->when retrnsmt   uid  timeout inode"}
	for i, row := range rows {
		lines = append(lines, fmt.Sprintf("%4d: %s %s %s 00000000:00000000 00:00000000 00000000 %5d        0 %d 1 0000000000000000 100 0 0 10 0",
			i, procAddr(t, row.local), procAddr(t, row.remote), row.state, row.uid, row.inode))
	}
	testutil.FailErr(t, "write "+name, os.WriteFile(filepath.Join(root, "net", name), []byte(strings.Join(lines, "\n")+"\n"), 0o644))
}

// holdSockets gives a fixture process descriptor links to socket inodes.
func holdSockets(t *testing.T, root string, pid int, inodes ...uint64) {
	t.Helper()
	dir := filepath.Join(root, strconv.Itoa(pid), "fd")
	testutil.FailErr(t, "mkdir fd", os.MkdirAll(dir, 0o755))
	testutil.FailErr(t, "stdin link", os.Symlink("/dev/null", filepath.Join(dir, "0")))
	for i, inode := range inodes {
		testutil.FailErr(t, "socket link", os.Symlink(fmt.Sprintf("socket:[%d]", inode), filepath.Join(dir, strconv.Itoa(i+3))))
	}
}

// procFixture is a procfs tree with TCP and UDP listeners on both families,
// two processes sharing one socket, and a holder whose descriptors are
// unreadable: its fd entry is not a directory, as the kernel answers for a
// process this user may not inspect.
func procFixture(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	testutil.FailErr(t, "mkdir net", os.MkdirAll(filepath.Join(root, "net"), 0o755))
	writeProcTable(t, root, "tcp",
		procRow{local: "127.0.0.1:5432", remote: "0.0.0.0:0", state: "0A", uid: 70, inode: 100},
		procRow{local: "127.0.0.1:41000", remote: "127.0.0.1:5432", state: "01", uid: 1000, inode: 101},
		procRow{local: "0.0.0.0:8080", remote: "0.0.0.0:0", state: "0A", uid: 1000, inode: 102},
		procRow{local: "0.0.0.0:9000", remote: "0.0.0.0:0", state: "0A", uid: 0, inode: 103},
	)
	writeProcTable(t, root, "tcp6",
		procRow{local: "[::]:8080", remote: "[::]:0", state: "0A", uid: 1000, inode: 104},
	)
	writeProcTable(t, root, "udp",
		procRow{local: "0.0.0.0:5353", remote: "0.0.0.0:0", state: "07", uid: 1000, inode: 105},
		procRow{local: "10.0.0.2:41001", remote: "10.0.0.1:53", state: "01", uid: 1000, inode: 106},
	)
	holdSockets(t, root, 300, 100)
	holdSockets(t, root, 301, 101)
	holdSockets(t, root, 400, 102, 104)
	holdSockets(t, root, 401, 102)
	holdSockets(t, root, 500, 105)
	testutil.FailErr(t, "mkdir unreadable", os.MkdirAll(filepath.Join(root, "600"), 0o755))
	testutil.FailErr(t, "unreadable fd", os.WriteFile(filepath.Join(root, "600", "fd"), nil, 0o000))
	testutil.FailErr(t, "self link", os.Symlink("600", filepath.Join(root, "self")))
	return root
}

func TestProcListenersJoinsTablesWithHolders(t *testing.T) {
	root := procFixture(t)
	ap := netip.MustParseAddrPort
	for _, tc := range []struct {
		name  string
		query ListenerQuery
		want  []Listener
	}{
		{name: "tcp port", query: ListenerQuery{Protocols: []Protocol{ProtocolTCP}, Port: 5432},
			want: []Listener{{Protocol: ProtocolTCP, Addr: ap("127.0.0.1:5432"), UID: 70, PIDs: []int{300}}}},
		{name: "shared socket on both families", query: ListenerQuery{Port: 8080}, want: []Listener{
			{Protocol: ProtocolTCP, Addr: ap("0.0.0.0:8080"), UID: 1000, PIDs: []int{400, 401}},
			{Protocol: ProtocolTCP, Addr: ap("[::]:8080"), UID: 1000, PIDs: []int{400}},
		}},
		{name: "unreadable holder keeps the port", query: ListenerQuery{Port: 9000},
			want: []Listener{{Protocol: ProtocolTCP, Addr: ap("0.0.0.0:9000"), UID: 0}}},
		{name: "unconnected udp", query: ListenerQuery{Protocols: []Protocol{ProtocolUDP}, Port: 5353},
			want: []Listener{{Protocol: ProtocolUDP, Addr: ap("0.0.0.0:5353"), UID: 1000, PIDs: []int{500}}}},
		{name: "udp query skips tcp", query: ListenerQuery{Protocols: []Protocol{ProtocolUDP}, Port: 5432}},
		{name: "connected sockets do not listen", query: ListenerQuery{Port: 41001}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := procFS{root: root}.listeners(tc.query)
			testutil.FailErr(t, "listeners", err)
			if len(got) == 0 && len(tc.want) == 0 {
				return
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("listeners(%+v) = %+v, want %+v", tc.query, got, tc.want)
			}
		})
	}
	all, err := procFS{root: root}.listeners(ListenerQuery{})
	testutil.FailErr(t, "all listeners", err)
	if len(all) != 5 {
		t.Fatalf("every listener on both protocols = %+v", all)
	}
}

func TestProcListenersWithoutIPv6TablesAndWithoutProc(t *testing.T) {
	root := procFixture(t)
	testutil.FailErr(t, "drop tcp6", os.Remove(filepath.Join(root, "net", "tcp6")))
	testutil.FailErr(t, "drop udp6", os.Remove(filepath.Join(root, "net", "udp6")))
	got, err := procFS{root: root}.listeners(ListenerQuery{Port: 8080})
	testutil.FailErr(t, "listeners without ipv6", err)
	if len(got) != 1 || got[0].Addr != netip.MustParseAddrPort("0.0.0.0:8080") {
		t.Fatalf("listeners without ipv6 = %+v", got)
	}
	if _, err := (procFS{root: filepath.Join(root, "missing")}).listeners(ListenerQuery{}); err == nil {
		t.Fatal("a missing tcp table reported no listeners instead of failing")
	}
	testutil.FailErr(t, "drop udp", os.Remove(filepath.Join(root, "net", "udp")))
	if _, err := (procFS{root: root}).listeners(ListenerQuery{Protocols: []Protocol{ProtocolUDP}}); err == nil {
		t.Fatal("a missing udp table reported no listeners instead of failing")
	}
}

func TestProcSocketHoldersFindsEveryHolder(t *testing.T) {
	root := procFixture(t)
	got, err := procFS{root: root}.socketHolders(map[uint64]bool{101: true, 102: true})
	testutil.FailErr(t, "socket holders", err)
	if want := map[uint64][]int{101: {301}, 102: {400, 401}}; !reflect.DeepEqual(got, want) {
		t.Fatalf("socketHolders = %v, want %v", got, want)
	}
	if got, err := (procFS{root: root}).socketHolders(nil); err != nil || len(got) != 0 {
		t.Fatalf("no wanted sockets = %v, %v", got, err)
	}
}

func TestResolveListenersSkipsUnreadableAndInvalidProcesses(t *testing.T) {
	rows := []kernelListener{
		{Protocol: ProtocolTCP, Local: netip.MustParseAddrPort("127.0.0.1:3000"), UID: 501, Handle: 7},
		{Protocol: ProtocolUDP, Local: netip.MustParseAddrPort("[::]:5353"), UID: 0, Handle: 9},
	}
	handles := map[int][]uint64{10: {7, 7, 3}, 11: {7}, 12: nil}
	got := resolveListeners(rows, []int{0, 10, 11, 12}, func(pid int) []uint64 {
		if pid <= 0 {
			t.Fatalf("walked invalid pid %d", pid)
		}
		return handles[pid]
	})
	want := []Listener{
		{Protocol: ProtocolTCP, Addr: rows[0].Local, UID: 501, PIDs: []int{10, 11}},
		{Protocol: ProtocolUDP, Addr: rows[1].Local, UID: 0},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("resolveListeners = %+v, want %+v", got, want)
	}
}
