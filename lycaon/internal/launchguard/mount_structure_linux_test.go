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

// selectionTopology is a small source namespace: the root, /usr holding a
// selected library root, kernel /proc, and a tmp directory other programs use.
func selectionTopology(t *testing.T, extra string) []mountEntry {
	t.Helper()
	entries, err := parseMountTopology([]byte(
		"2 1 8:1 / / rw - ext4 /dev/root rw\n" +
			"3 2 8:2 / /usr ro - ext4 /dev/usr ro\n" +
			"5 3 0:12 / /usr/lib/generation ro - tmpfs none ro\n" +
			"7 2 0:14 / /proc rw - proc proc rw\n" +
			"10 2 0:20 / /tmp rw - tmpfs tmpfs rw\n" + extra))
	testutil.FailErr(t, "decode selection topology", err)
	return entries
}

func TestSelectionIgnoresUnrelatedHostMountsAndRefusesChangesItDependsOn(t *testing.T) {
	selected := map[int]bool{2: true, 3: true, 5: true, 7: true}
	paths := []string{"/usr/lib/generation", "/usr/lib", "/", "/proc"}
	before := selectionTopology(t, "11 10 0:30 / /tmp/fixture/004 ro - fuse.fixture fixture ro\n")
	for name, after := range map[string]string{
		"unrelated mount removed":      "",
		"unrelated mount added":        "11 10 0:30 / /tmp/fixture/004 ro - fuse.fixture fixture ro\n12 10 0:31 / /tmp/other ro - fuse.fixture fixture ro\n",
		"mount beneath a library root": "11 10 0:30 / /tmp/fixture/004 ro - fuse.fixture fixture ro\n13 5 0:32 / /usr/lib/generation/nested ro - tmpfs none ro\n",
	} {
		if !sameSelectionTopology(before, selectionTopology(t, after), selected, paths) {
			t.Errorf("%s refused a selection it does not affect", name)
		}
	}
	for name, after := range map[string][]mountEntry{
		"selected mount changed": func() []mountEntry {
			entries := selectionTopology(t, "11 10 0:30 / /tmp/fixture/004 ro - fuse.fixture fixture ro\n")
			entries[2].options = "rw"
			return entries
		}(),
		"selected mount removed": selectionTopology(t, "11 10 0:30 / /tmp/fixture/004 ro - fuse.fixture fixture ro\n")[:2],
		"cover above a resolved path": append(selectionTopology(t, "11 10 0:30 / /tmp/fixture/004 ro - fuse.fixture fixture ro\n"),
			mountEntry{id: 14, parent: 3, device: "0:33", root: "/", mountPoint: "/usr/lib", filesystem: "tmpfs"}),
		"cover over the root": append(selectionTopology(t, "11 10 0:30 / /tmp/fixture/004 ro - fuse.fixture fixture ro\n"),
			mountEntry{id: 15, parent: 2, device: "0:34", root: "/", mountPoint: "/", filesystem: "tmpfs"}),
	} {
		if sameSelectionTopology(before, after, selected, paths) {
			t.Errorf("%s was accepted", name)
		}
	}
}

func TestVanishedInitialMountsArePrunedOnlyWhenNonStructural(t *testing.T) {
	staging := selectionTopology(t, "11 10 0:30 / /tmp/fixture/004 ro - fuse.fixture fixture ro\n")
	structural := map[int]bool{2: true, 3: true, 5: true, 7: true}
	removed := map[int]bool{}
	if !acceptVanishedMounts(staging, selectionTopology(t, ""), removed, structural) || !removed[11] || len(removed) != 1 {
		t.Fatalf("a non-structural mount the kernel detached was not pruned: %v", removed)
	}
	for name, current := range map[string][]mountEntry{
		"structural mount vanished": func() []mountEntry {
			entries := selectionTopology(t, "11 10 0:30 / /tmp/fixture/004 ro - fuse.fixture fixture ro\n")
			return append(entries[:2], entries[3:]...)
		}(),
		"mount added": selectionTopology(t, "11 10 0:30 / /tmp/fixture/004 ro - fuse.fixture fixture ro\n12 10 0:31 / /tmp/other ro - tmpfs none ro\n"),
		"mount changed": func() []mountEntry {
			entries := selectionTopology(t, "11 10 0:30 / /tmp/fixture/004 ro - fuse.fixture fixture ro\n")
			entries[4].options = "ro"
			return entries
		}(),
	} {
		removed := map[int]bool{}
		if acceptVanishedMounts(staging, current, removed, structural) || len(removed) != 0 {
			t.Errorf("%s was accepted, removed=%v", name, removed)
		}
	}
}

func TestKernelDetachedMountIsAbsentFromTheTopologyOnly(t *testing.T) {
	_, entries, err := readCurrentMountTopology()
	testutil.FailErr(t, "read this namespace", err)
	if detached, err := kernelDetachedMount(uint64(entries[0].id)); err != nil || detached {
		t.Fatalf("a listed mount reads as detached: %v %v", detached, err)
	}
	highest := 0
	for _, entry := range entries {
		highest = max(highest, entry.id)
	}
	if detached, err := kernelDetachedMount(uint64(highest) + 1); err != nil || !detached {
		t.Fatalf("an unlisted mount does not read as detached: %v %v", detached, err)
	}
}
