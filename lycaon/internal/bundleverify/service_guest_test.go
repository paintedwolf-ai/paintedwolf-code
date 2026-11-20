package bundleverify

import (
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeGuestAssets(t *testing.T, app string) {
	t.Helper()
	root := filepath.Join(app, serviceGuestDir)
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
			if got := checkServiceGuestAssets(app); len(got) != 0 {
				t.Fatalf("valid assets: %v", got)
			}
			if err := os.WriteFile(filepath.Join(app, serviceGuestDir, name), []byte("changed"), 0644); err != nil {
				t.Fatal(err)
			}
			if got := checkServiceGuestAssets(app); len(got) != 1 || got[0].Path != filepath.Join(serviceGuestDir, name) {
				t.Fatalf("changed asset: %v", got)
			}
		})
	}
}
