package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/diskfs/go-diskfs/backend/file"
	"github.com/diskfs/go-diskfs/filesystem/squashfs"
)

func TestPinnedBootDrivers(t *testing.T) {
	for _, tc := range []struct {
		name    string
		builtin string
		valid   bool
	}{
		{"built-in console", "kernel/virtio_pci.ko\nkernel/virtio_console.ko\n", true},
		{"missing console", "kernel/virtio_pci.ko\n", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			image := filepath.Join(t.TempDir(), "modloop")
			f, err := os.Create(image)
			if err != nil {
				t.Fatal(err)
			}
			defer f.Close()
			tree, err := squashfs.Create(file.New(f, false), 0, 0, 4096)
			if err != nil {
				t.Fatal(err)
			}
			root := filepath.Join(tree.Workspace(), "modules", "test-virt")
			if err := os.MkdirAll(root, 0755); err != nil {
				t.Fatal(err)
			}
			files := map[string]string{
				"modules.dep":     "virtio-rng.ko: bus.ko\nvmw_vsock_virtio_transport.ko: bus.ko\nbus.ko:\n",
				"modules.builtin": tc.builtin,
				"bus.ko":          "bus", "virtio-rng.ko": "entropy", "vmw_vsock_virtio_transport.ko": "socket",
			}
			for name, contents := range files {
				if err := os.WriteFile(filepath.Join(root, name), []byte(contents), 0644); err != nil {
					t.Fatal(err)
				}
			}
			if err := tree.Finalize(squashfs.FinalizeOptions{}); err != nil {
				t.Fatal(err)
			}
			output := t.TempDir()
			err = extract(image, output)
			if (err == nil) != tc.valid {
				t.Fatalf("extract returned %v", err)
			}
			if tc.valid {
				for name, want := range map[string]string{"01-bus.ko": "bus", "02-virtio-rng.ko": "entropy", "03-vmw_vsock_virtio_transport.ko": "socket"} {
					data, err := os.ReadFile(filepath.Join(output, name))
					if err != nil || string(data) != want {
						t.Fatalf("%s: got %q, %v", name, data, err)
					}
				}
			}
		})
	}
}
