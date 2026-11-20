// Command modules extracts the guest's boot drivers from the pinned release image.
package main

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"strings"

	"github.com/diskfs/go-diskfs/backend/file"
	"github.com/diskfs/go-diskfs/filesystem/squashfs"
)

func main() {
	if len(os.Args) != 3 {
		fmt.Fprintln(os.Stderr, "usage: modules MODLOOP OUTPUT")
		os.Exit(2)
	}
	if err := extract(os.Args[1], os.Args[2]); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func extract(image, output string) error {
	source, err := file.OpenFromPath(image, true)
	if err != nil {
		return err
	}
	defer source.Close()
	info, err := source.Stat()
	if err != nil {
		return err
	}
	tree, err := squashfs.Read(source, info.Size(), 0, 0)
	if err != nil {
		return err
	}
	versions, err := tree.ReadDir("/modules")
	if err != nil {
		return err
	}
	root := ""
	for _, v := range versions {
		if v.IsDir() && strings.HasSuffix(v.Name(), "-virt") {
			if root != "" {
				return fmt.Errorf("multiple kernel module trees")
			}
			root = path.Join("/modules", v.Name())
		}
	}
	if root == "" {
		return fmt.Errorf("missing kernel module tree")
	}
	index, err := tree.OpenFile(path.Join(root, "modules.dep"), os.O_RDONLY)
	if err != nil {
		return err
	}
	defer index.Close()
	names, deps := map[string]string{}, map[string][]string{}
	lines := bufio.NewScanner(index)
	for lines.Scan() {
		name, required, ok := strings.Cut(lines.Text(), ":")
		if !ok {
			return fmt.Errorf("invalid module dependency")
		}
		names[path.Base(name)] = name
		deps[name] = strings.Fields(required)
	}
	if err := lines.Err(); err != nil {
		return err
	}
	seen := map[string]bool{}
	var write func(string) error
	write = func(name string) error {
		if seen[name] {
			return nil
		}
		if _, ok := deps[name]; !ok {
			return fmt.Errorf("missing boot module %q", name)
		}
		for _, dep := range deps[name] {
			if err := write(dep); err != nil {
				return err
			}
		}
		r, err := tree.OpenFile(path.Join(root, name), os.O_RDONLY)
		if err != nil {
			return err
		}
		defer r.Close()
		data, err := io.ReadAll(r)
		if err != nil {
			return err
		}
		seen[name] = true
		return os.WriteFile(filepath.Join(output, fmt.Sprintf("%02d-%s", len(seen), path.Base(name))), data, 0644)
	}
	for _, name := range []string{"virtio-rng.ko", "vmw_vsock_virtio_transport.ko"} {
		if err := write(names[name]); err != nil {
			return err
		}
	}
	return nil
}
