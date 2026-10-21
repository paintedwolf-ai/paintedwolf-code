package pkgmanifest_test

import (
	"github.com/lycaon/lycaon/internal/pkgmanifest"
	"os"
	"path/filepath"
	"testing"
)

func TestRetainedPayloadVerificationRejectsChangedResourcesAndInventory(t *testing.T) {
	cases := map[string]func(*testing.T, string){
		"intact": func(t *testing.T, root string) {},
		"extra resource": func(t *testing.T, root string) {
			if err := os.WriteFile(filepath.Join(root, "unlisted.data"), []byte("resource"), 0644); err != nil {
				t.Fatal(err)
			}
		},
		"changed image": func(t *testing.T, root string) {
			if err := os.WriteFile(filepath.Join(root, "bin/pw"), []byte("replacement"), 0755); err != nil {
				t.Fatal(err)
			}
		},
		"changed mode": func(t *testing.T, root string) {
			if err := os.Chmod(filepath.Join(root, "bin/pw"), 0777); err != nil {
				t.Fatal(err)
			}
		},
		"missing image": func(t *testing.T, root string) {
			if err := os.Remove(filepath.Join(root, "bin/pw")); err != nil {
				t.Fatal(err)
			}
		},
		"redirected image": func(t *testing.T, root string) {
			name := filepath.Join(root, "bin/pw")
			if err := os.Remove(name); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink("../manifest.json", name); err != nil {
				t.Fatal(err)
			}
		},
	}
	for name, change := range cases {
		t.Run(name, func(t *testing.T) {
			file, _ := fixture(t, nil)
			m, err := pkgmanifest.Load(file)
			if err != nil {
				t.Fatal(err)
			}
			root := filepath.Dir(file)
			change(t, root)
			retained, err := os.OpenRoot(root)
			if err != nil {
				t.Fatal(err)
			}
			defer retained.Close()
			err = m.VerifyPayload(retained)
			if (name == "intact") != (err == nil) {
				t.Fatalf("VerifyPayload()=%v", err)
			}
		})
	}
}

func TestPayloadVerificationKeepsTheRetainedRootAndRefusesClosedRoots(t *testing.T) {
	file, _ := fixture(t, nil)
	m, err := pkgmanifest.Load(file)
	if err != nil {
		t.Fatal(err)
	}
	root, err := os.OpenRoot(filepath.Dir(file))
	if err != nil {
		t.Fatal(err)
	}
	if err := m.VerifyPayload(root); err != nil {
		t.Fatal(err)
	}
	if _, err := root.Stat("bin/pw"); err != nil {
		t.Fatal("verification closed caller root", err)
	}
	if err := root.Close(); err != nil {
		t.Fatal(err)
	}
	if err := m.VerifyPayload(root); err == nil {
		t.Fatal("accepted closed root")
	}
	if err := m.VerifyPayload(nil); err == nil {
		t.Fatal("accepted missing root")
	}
}
