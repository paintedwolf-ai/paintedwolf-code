package bundleverify

import (
	"crypto/sha256"
	"debug/macho"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
)

func writeGuestAssets(t *testing.T, app string) {
	t.Helper()
	root := filepath.Join(app, serviceGuestDir)
	writeThinMachO(t, filepath.Join(app, serviceGuestHelper), macho.CpuArm64, "14.0", nil)
	image := []byte("guest fixture")
	for _, name := range []string{"kernel", "initramfs.gz"} {
		mustWriteFile(t, filepath.Join(root, name), image)
	}
	d := sha256.Sum256(image)
	mustWriteFile(t, filepath.Join(root, "manifest.json"), []byte(fmt.Sprintf(`{"schema":1,"architecture":"arm64","kernel_sha256":"%x","initramfs_sha256":"%x"}`, d, d)))
	mustWriteFile(t, filepath.Join(root, "pw-service-vm.cdhash"), []byte(strings.Repeat("a", 40)+"\n"))
}

func TestPrivateServiceGuestIntegrity(t *testing.T) {
	for _, name := range []string{"kernel", "initramfs.gz", "manifest.json", "pw-service-vm.cdhash"} {
		t.Run(name, func(t *testing.T) {
			app := t.TempDir()
			writeGuestAssets(t, app)
			images, err := WalkMachO(app)
			testutil.FailErr(t, "inspect guest helper", err)
			if got := checkServiceGuestAssets(app, images); len(got) != 0 {
				t.Fatalf("valid assets: %v", got)
			}
			testutil.FailErr(t, "replace guest asset", os.WriteFile(filepath.Join(app, serviceGuestDir, name), []byte("changed"), 0644))
			if got := checkServiceGuestAssets(app, images); len(got) != 1 || got[0].Path != filepath.Join(serviceGuestDir, name) {
				t.Fatalf("changed asset: %v", got)
			}
		})
	}
}

func guestCodesignFixture(description string) []byte {
	return []byte(strings.ReplaceAll(description, "Identifier=dev.paintedwolf.code\n", "Identifier=dev.paintedwolf.code.service-vm\n") + "CDHash=" + strings.Repeat("a", 40) + "\n")
}
func TestPrivateServiceGuestRejectsMismatchedDistribution(t *testing.T) {
	app := t.TempDir()
	writeGuestAssets(t, app)
	images, err := WalkMachO(app)
	testutil.FailErr(t, "inspect packaged helper", err)
	if found := checkServiceGuestAssets(app, nil); len(found) != 1 || found[0].Path != serviceGuestHelper {
		t.Fatalf("absent native helper: %v", found)
	}
	images[0].Slices = append(images[0].Slices, SliceFacts{GoArch: "amd64"})
	if found := checkServiceGuestAssets(app, images); len(found) != 1 || found[0].Path != serviceGuestHelper {
		t.Fatalf("multi-architecture helper: %v", found)
	}
	path := filepath.Join(app, serviceGuestDir, "manifest.json")
	data, err := os.ReadFile(path)
	testutil.FailErr(t, "read manifest", err)
	testutil.FailErr(t, "stage incorrect image architecture", os.WriteFile(path, []byte(strings.Replace(string(data), "arm64", "amd64", 1)), 0644))
	if found := checkServiceGuestAssets(app, images); len(found) != 1 || found[0].Path != filepath.Join(serviceGuestDir, "manifest.json") {
		t.Fatalf("unshipped image: %v", found)
	}
	display := guestCodesignFixture(codesignDeveloperIDWithRuntime)
	if found := checkGuestHelperSignature(app, display); len(found) != 0 {
		t.Fatal(found)
	}
	if found := checkGuestHelperSignature(app, []byte(strings.Replace(string(display), "dev.paintedwolf.code.service-vm", "dev.fixture", 1))); len(found) != 1 {
		t.Fatal(found)
	}
	if found := checkGuestHelperSignature(app, []byte(strings.Replace(string(display), strings.Repeat("a", 40), strings.Repeat("b", 40), 1))); len(found) != 1 {
		t.Fatal(found)
	}
}
