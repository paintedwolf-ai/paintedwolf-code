//go:build darwin || linux

package db

import (
	"os"
	"path/filepath"

	"golang.org/x/sys/unix"
)

func upgradeTempDirectories(configured string) ([]string, error) {
	candidates := []string{configured, os.Getenv("SQLITE_TMPDIR"), os.Getenv("TMPDIR"), "/var/tmp", "/usr/tmp", "/tmp", "."}
	var out []string
	seen := map[string]bool{}
	for _, candidate := range candidates {
		if candidate == "" {
			continue
		}
		root, err := filepath.Abs(candidate)
		if err != nil {
			return nil, err
		}
		info, err := os.Stat(root)
		if err != nil || !info.IsDir() || unix.Access(root, 3) != nil {
			continue
		}
		if !seen[root] {
			out = append(out, root)
			seen[root] = true
		}
	}
	return out, nil
}
