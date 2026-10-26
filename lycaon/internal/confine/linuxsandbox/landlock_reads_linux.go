//go:build linux

package linuxsandbox

import (
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"golang.org/x/sys/unix"
)

// maxReadRules bounds the read rules one launch adds for read-denied paths
// that do not exist yet.
const maxReadRules = 4096

// readRules grants reading and executing everywhere except where a
// read-denied path would appear. An existing denied path is covered by its
// placeholder mount, but a mount needs an existing mountpoint, so one that
// does not exist yet is held by Landlock instead: each directory on the way to
// it may only be listed, and every entry beside that way stays readable. A
// denied path another process creates later is then visible by name but
// unreadable, and so is anything else created after launch in those
// directories. A denied path inside a write root keeps that root's grant; the
// creation floors still refuse the sandbox making it.
func readRules(fs Filesystem) ([]landlockRule, error) {
	chain := map[string]map[string]bool{}
	for _, hidden := range fs.Hidden {
		hidden = filepath.Clean(hidden)
		if _, err := os.Lstat(hidden); !errors.Is(err, os.ErrNotExist) || underAny(hidden, fs.WriteRoots) {
			continue
		}
		for dir, next := filepath.Dir(hidden), filepath.Base(hidden); ; dir, next = filepath.Dir(dir), filepath.Base(dir) {
			if chain[dir] == nil {
				chain[dir] = map[string]bool{}
			}
			chain[dir][next] = true
			if dir == "/" {
				break
			}
		}
	}
	if len(chain) == 0 {
		return []landlockRule{{path: "/", access: landlockReadExecute}}, nil
	}
	dirs := make([]string, 0, len(chain))
	for dir := range chain {
		dirs = append(dirs, dir)
	}
	sort.Strings(dirs)
	var rules []landlockRule
	for _, dir := range dirs {
		if _, err := os.Lstat(dir); err != nil {
			// A directory on the way that does not exist holds nothing yet.
			continue
		}
		rules = append(rules, landlockRule{path: dir, access: unix.LANDLOCK_ACCESS_FS_READ_DIR})
		entries, err := os.ReadDir(dir)
		if err != nil {
			return nil, &Unsupported{Code: UnsupportedFilesystemRule, Detail: "list " + dir + " for a read-denied path: " + err.Error()}
		}
		for _, entry := range entries {
			if chain[dir][entry.Name()] || entry.Type()&os.ModeSymlink != 0 {
				// The way on is ruled itself; a link is ruled where it points.
				continue
			}
			rules = append(rules, landlockRule{path: joinName(dir, entry.Name()), access: landlockReadExecute})
		}
		if len(rules) > maxReadRules {
			return nil, &Unsupported{Code: UnsupportedFilesystemRule, Detail: "read-denied paths absent at launch need more than " + strconv.Itoa(maxReadRules) + " read rules"}
		}
	}
	return rules, nil
}

func underAny(path string, roots []string) bool {
	for _, root := range roots {
		root = filepath.Clean(root)
		if path == root || strings.HasPrefix(path, strings.TrimSuffix(root, "/")+"/") {
			return true
		}
	}
	return false
}
