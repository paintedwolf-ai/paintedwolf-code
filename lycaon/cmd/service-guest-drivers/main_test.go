package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/diskfs/go-diskfs/backend/file"
	"github.com/diskfs/go-diskfs/filesystem/squashfs"
	"github.com/lycaon/lycaon/internal/testutil"
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
			testutil.FailErr(t, "create kernel module image", err)
			defer f.Close()
			tree, err := squashfs.Create(file.New(f, false), 0, 0, 4096)
			testutil.FailErr(t, "create squashfs fixture", err)
			root := filepath.Join(tree.Workspace(), "modules", "test-virt")
			testutil.FailErr(t, "create kernel module directory", os.MkdirAll(root, 0755))
			files := map[string]string{
				"modules.dep":     "virtio-rng.ko: bus.ko\nvmw_vsock_virtio_transport.ko: bus.ko\nbus.ko:\n",
				"modules.builtin": tc.builtin,
				"bus.ko":          "bus", "virtio-rng.ko": "entropy", "vmw_vsock_virtio_transport.ko": "socket",
			}
			for name, contents := range files {
				testutil.FailErr(t, "write kernel module fixture", os.WriteFile(filepath.Join(root, name), []byte(contents), 0644))
			}
			testutil.FailErr(t, "finalize squashfs image", tree.Finalize(squashfs.FinalizeOptions{}))
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
