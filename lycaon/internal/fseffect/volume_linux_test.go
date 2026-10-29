package fseffect

import "testing"

func TestMountinfoEntriesDecode(t *testing.T) {
	for field, want := range map[string]string{`/mnt/a\040b`: "/mnt/a b", `/x\134y`: `/x\y`, `/plain`: "/plain", `/end\04`: `/end\04`} {
		if got := unescapeMount(field); got != want {
			t.Errorf("unescapeMount(%q) = %q, want %q", field, got, want)
		}
	}
	line := `36 35 98:0 /mnt1 /mnt/data\040disk rw,noatime master:1 - ext4 /dev/sda\0401 rw`
	if target, source, ok := mountEntry(line, "98:0"); !ok || target != "/mnt/data disk" || source != "/dev/sda 1" {
		t.Fatalf("mountEntry = %q %q %v", target, source, ok)
	}
	if _, _, ok := mountEntry(line, "8:1"); ok {
		t.Fatal("mount on another device matched")
	}
	if !mountHolds("/", "/x") || !mountHolds("/mnt", "/mnt/a") || mountHolds("/mnt", "/mntx") {
		t.Fatal("mount containment")
	}
}
