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
	addChain := func(target string) {
		for dir, next := filepath.Dir(target), filepath.Base(target); ; dir, next = filepath.Dir(dir), filepath.Base(dir) {
			if chain[dir] == nil {
				chain[dir] = map[string]bool{}
			}
			chain[dir][next] = true
			if dir == "/" {
				break
			}
		}
	}
	for _, hidden := range fs.Hidden {
		hidden = filepath.Clean(hidden)
		if _, err := os.Lstat(hidden); !errors.Is(err, os.ErrNotExist) || underAny(hidden, fs.WriteRoots) {
			continue
		}
		addChain(hidden)
	}
	for _, wo := range append(append([]string(nil), fs.WriteOnlyRoots...), fs.WriteOnlyFiles...) {
		wo = filepath.Clean(wo)
		if _, err := os.Lstat(wo); err == nil {
			addChain(wo)
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
		dirReadable := readDirAllowed(fs, dir)
		if dirReadable {
			rules = append(rules, landlockRule{path: dir, access: unix.LANDLOCK_ACCESS_FS_READ_DIR})
		}
		entries, err := os.ReadDir(dir)
		if err != nil {
			return nil, &Unsupported{Code: UnsupportedFilesystemRule, Detail: "list " + dir + " for a read-denied path: " + err.Error()}
		}
		for _, entry := range entries {
			if chain[dir][entry.Name()] || entry.Type()&os.ModeSymlink != 0 {
				// The way on is ruled itself; a link is ruled where it points.
				continue
			}
			child := joinName(dir, entry.Name())
			if !dirReadable && !explicitReadEntry(fs, child) {
				continue
			}
			rules = append(rules, landlockRule{path: child, access: landlockReadExecute})
		}
		if len(rules) > maxReadRules {
			return nil, &Unsupported{Code: UnsupportedFilesystemRule, Detail: "read-denied paths absent at launch need more than " + strconv.Itoa(maxReadRules) + " read rules"}
		}
	}
	return rules, nil
}

func readDirAllowed(fs Filesystem, dir string) bool {
	if underAny(dir, fs.WriteOnlyRoots) {
		return underDeeper(dir, fs.ReadRoots, fs.WriteOnlyRoots)
	}
	if underAny(dir, fs.Hidden) {
		return underDeeper(dir, fs.ReadRoots, fs.Hidden)
	}
	return true
}

func explicitReadEntry(fs Filesystem, path string) bool {
	for _, f := range fs.ReadFiles {
		if filepath.Clean(f) == path {
			return true
		}
	}
	return underAny(path, fs.ReadRoots)
}

func underDeeper(path string, allow, deny []string) bool {
	bestAllow, bestDeny := -1, -1
	for _, a := range allow {
		a = filepath.Clean(a)
		if underAny(path, []string{a}) && len(a) > bestAllow {
			bestAllow = len(a)
		}
	}
	for _, d := range deny {
		d = filepath.Clean(d)
		if underAny(path, []string{d}) && len(d) > bestDeny {
			bestDeny = len(d)
		}
	}
	return bestAllow > bestDeny
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
