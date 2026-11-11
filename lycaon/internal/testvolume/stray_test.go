//go:build darwin

package testvolume

import (
	"os"
	"strings"
	"testing"
)

func TestStrayVolumesSelectsOnlyGonePlacementVolumes(t *testing.T) {
	info := strings.Join([]string{
		"image-path      : /System/Library/AssetsV2/com_apple_MobileAsset_MetalToolchain/AssetData/Restore/022-21521-014.dmg",
		"/dev/disk4\tGUID_partition_scheme\t",
		"/dev/disk4s1\t7C3457EF-0000-11AA-AA11-00306543ECAC\t",
		"/dev/disk5s1\t41504653-0000-11AA-AA11-00306543ECAC\t/private/var/run/com.apple.security.cryptexd/mnt/com.apple.MobileAsset.MetalToolchain",
		"image-path      : /tmp/pwplacement-probe/pwplacement-4242.dmg",
		"/dev/disk6\tGUID_partition_scheme\t",
		"/dev/disk7s1\t41504653-0000-11AA-AA11-00306543ECAC\t/tmp/pwplacement-probe/pwplacement-4242",
		"image-path      : /tmp/pwplacement-probe/pwplacement-4243.dmg",
		"/dev/disk8s1\t41504653-0000-11AA-AA11-00306543ECAC\t/tmp/pwplacement-probe/pwplacement-4243",
		"image-path      : /tmp/pwplacement-probe/pwplacement-4244.dmg",
	}, "\n")
	gone := func(pid int) bool { return pid != 4243 }
	mounts, images := strayVolumes(info, gone)
	if len(mounts) != 1 || mounts[0] != "/tmp/pwplacement-probe/pwplacement-4242" {
		t.Fatalf("mounts = %v, want the gone volume's mount point", mounts)
	}
	wantImages := []string{
		"/tmp/pwplacement-probe/pwplacement-4242.dmg",
		"/tmp/pwplacement-probe/pwplacement-4244.dmg",
	}
	if len(images) != 2 || images[0] != wantImages[0] || images[1] != wantImages[1] {
		t.Fatalf("images = %v, want %v", images, wantImages)
	}
}

func TestStrayVolumesMatchesMountPointsWithoutAnImageLine(t *testing.T) {
	info := "/dev/disk9s1\t41504653-0000-11AA-AA11-00306543ECAC\t/tmp/pwplacement-probe/pwplacement-4245"
	mounts, images := strayVolumes(info, func(int) bool { return true })
	if len(mounts) != 1 || mounts[0] != "/tmp/pwplacement-probe/pwplacement-4245" {
		t.Fatalf("mounts = %v", mounts)
	}
	if len(images) != 0 {
		t.Fatalf("images = %v", images)
	}
}

func TestProcessGoneReportsThisProcessAndDeadOnes(t *testing.T) {
	if !processGone(os.Getpid()) {
		t.Fatal("this process is not gone")
	}
	if processGone(1) {
		t.Fatal("pid 1 is gone")
	}
	if !processGone(1 << 30) {
		t.Fatal("a pid no process holds is not gone")
	}
}

func TestSweepStraysLeavesTheHostAloneWhenNothingMatches(t *testing.T) {
	sweepStrays()
}
