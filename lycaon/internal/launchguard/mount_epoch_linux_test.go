//go:build linux

package launchguard

import (
	"reflect"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
)

func TestMountEpochPreservesExactStructureAcrossNamespaceIDs(t *testing.T) {
	original, err := parseMountTopology([]byte("2 1 8:1 / / rw shared:1 - ext4 /dev/root rw\n3 2 0:1 / /proc rw shared:2 - proc proc rw\n4 2 8:2 / /volume rw - ext4 /dev/volume rw\n"))
	testutil.FailErr(t, "decode source topology", err)
	current := append([]mountEntry(nil), original...)
	for index := range current {
		current[index].id += 100
		if index > 0 {
			current[index].parent += 100
		}
		current[index].shared, current[index].master = 0, index+1
	}
	got, err := correlateStructuralMounts(original, []int{2, 3}, current)
	testutil.FailErr(t, "map selected source topology", err)
	if !reflect.DeepEqual(got, map[int]bool{102: true, 103: true}) {
		t.Fatalf("mapped structural IDs %v", got)
	}
	current[1].parent = current[2].id
	if _, err := correlateStructuralMounts(original, []int{2, 3}, current); err == nil {
		t.Fatal("accepted identical proc facts with a different parent")
	}
	current[1].parent = current[0].id
	duplicate := current[1]
	duplicate.id = 200
	if _, err := correlateStructuralMounts(original, []int{2, 3}, append(current, duplicate)); err == nil {
		t.Fatal("accepted ambiguous structural source correspondence")
	}
}

func TestMountEpochDetectsUnselectedChangesAndOrdersCoversFirst(t *testing.T) {
	entries, err := parseMountTopology([]byte("2 1 8:1 / / rw - ext4 /dev/root rw\n3 2 8:2 / /volume rw - ext4 /dev/volume rw\n4 3 0:1 / /volume/nested rw - tmpfs none rw\n5 3 8:3 / /volume rw - ext4 /dev/cover rw\n"))
	testutil.FailErr(t, "decode nested and covered volumes", err)
	order := volumeDetachOrder(entries, map[int]bool{2: true})
	if len(order) != 3 || order[2].id != 3 || order[0].id != 5 || order[1].id != 4 {
		t.Fatalf("unsafe detach order: %+v", order)
	}
	if !sameMountEpoch(entries, entries[:2], map[int]bool{4: true, 5: true}) {
		t.Fatal("refused precisely accounted removals")
	}
	changed := append([]mountEntry(nil), entries...)
	changed[2].root = "/replaced"
	if sameMountEpoch(entries, changed, nil) {
		t.Fatal("ignored an unselected child's source replacement")
	}
	if sameMountEpoch(entries, append(changed, changed[0]), nil) {
		t.Fatal("ignored a duplicate or additional mount")
	}
}
