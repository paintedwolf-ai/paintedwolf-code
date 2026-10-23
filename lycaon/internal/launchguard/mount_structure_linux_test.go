//go:build linux

package launchguard

import (
	"reflect"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
)

func TestMountStructureUsesExactRolesAndAncestorsOnly(t *testing.T) {
	entries, err := parseMountTopology([]byte(
		"2 1 8:1 / / rw - ext4 /dev/root rw\n" +
			"3 2 8:2 / /usr ro - ext4 /dev/usr ro\n" +
			"4 2 8:3 / /home rw - ext4 /dev/fixture rw\n" +
			"5 3 0:12 / /usr/lib/generation ro - tmpfs none ro\n" +
			"6 3 0:13 / /usr/nested-volume rw,noexec - tmpfs none rw\n" +
			"7 2 0:14 / /proc rw - proc proc rw\n" +
			"8 7 0:15 / /proc/mask ro - tmpfs none ro\n" +
			"9 4 8:3 /shared /fixture/bind rw - ext4 /dev/fixture rw\n"))
	testutil.FailErr(t, "decode role topology", err)
	got, err := structuralAncestors(entries, map[int]bool{5: true, 7: true})
	testutil.FailErr(t, "select exact role ancestors", err)
	if want := map[int]bool{2: true, 3: true, 5: true, 7: true}; !reflect.DeepEqual(got, want) {
		t.Fatalf("structural IDs %v, want %v", got, want)
	}
	if _, err := structuralAncestors(entries, map[int]bool{99: true}); err == nil {
		t.Fatal("accepted a role absent from the topology")
	}
}
