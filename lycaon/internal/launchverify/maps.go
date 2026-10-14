package launchverify

import (
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// CodeForeignObject: the process maps executable code from a file that a
// login user could have written, or from memory with no file behind it that
// names itself, such as a memfd.
const CodeForeignObject Code = "foreign_object"

// fileFacts is what the mapping check needs to know about one path.
type fileFacts struct {
	inode uint64
	uid   uint32
	mode  fs.FileMode
}

// mapping is one executable file-backed region of a process.
type mapping struct {
	inode uint64
	path  string
}

const maxMapsBytes = 16 << 20

// VerifyMappings proves that every file the process maps executable is a
// system object: owned by root, not writable by group or others, in
// directories with the same properties, and still the inode the process
// mapped. Packaged executables and distribution libraries pass; a library a
// user preloaded or planted does not.
func (v Verifier) VerifyMappings(pid int, role string) error {
	raw, err := readBounded(v.procPath(pid, "maps"), maxMapsBytes)
	if err != nil {
		return refuse(CodeProcessGone, role, pid, err.Error())
	}
	mappings, err := executableMappings(raw)
	if err != nil {
		return refuse(CodeForeignObject, role, pid, err.Error())
	}
	stat := v.statFile
	if stat == nil {
		stat = lstatFacts
	}
	checked := map[string]bool{}
	for _, m := range mappings {
		if checked[m.path] {
			continue
		}
		if err := systemObject(stat, m); err != nil {
			return refuse(CodeForeignObject, role, pid, err.Error())
		}
		checked[m.path] = true
	}
	return nil
}

// executableMappings parses maps lines of the form
// `start-end perms offset dev inode path`.
func executableMappings(raw []byte) ([]mapping, error) {
	var out []mapping
	for _, line := range strings.Split(string(raw), "\n") {
		if line == "" {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 5 {
			return nil, fmt.Errorf("maps line is truncated: %q", line)
		}
		if !strings.Contains(fields[1], "x") {
			continue
		}
		inode, err := strconv.ParseUint(fields[4], 10, 64)
		if err != nil {
			return nil, fmt.Errorf("maps inode: %w", err)
		}
		path := ""
		if len(fields) > 5 {
			// The path is the rest of the line and may contain spaces.
			path = strings.TrimLeft(line[strings.Index(line, fields[5]):], " ")
		}
		switch {
		case inode == 0 && (path == "" || strings.HasPrefix(path, "[")):
			// Anonymous code and kernel pseudo-mappings carry no file.
			continue
		case strings.HasPrefix(path, "/memfd:"):
			return nil, fmt.Errorf("executable memfd mapping %q", path)
		case strings.HasSuffix(path, " (deleted)"):
			return nil, fmt.Errorf("executable mapping of a deleted file %q", path)
		case !filepath.IsAbs(path) || inode == 0:
			return nil, fmt.Errorf("executable mapping without a file %q", path)
		}
		out = append(out, mapping{inode: inode, path: path})
	}
	return out, nil
}

// systemObject checks a mapped file and every directory above it.
func systemObject(stat func(string) (fileFacts, error), m mapping) error {
	facts, err := stat(m.path)
	if err != nil {
		return fmt.Errorf("%s: %w", m.path, err)
	}
	if !facts.mode.IsRegular() {
		return fmt.Errorf("%s is not a regular file", m.path)
	}
	if facts.inode != m.inode {
		return fmt.Errorf("%s is no longer the mapped file", m.path)
	}
	if err := rootOwned(m.path, facts); err != nil {
		return err
	}
	for dir := filepath.Dir(m.path); ; dir = filepath.Dir(dir) {
		facts, err := stat(dir)
		if err != nil {
			return fmt.Errorf("%s: %w", dir, err)
		}
		if !facts.mode.IsDir() {
			return fmt.Errorf("%s is not a directory", dir)
		}
		if err := rootOwned(dir, facts); err != nil {
			return err
		}
		if dir == "/" {
			return nil
		}
	}
}

func rootOwned(path string, facts fileFacts) error {
	if facts.uid != 0 {
		return fmt.Errorf("%s is owned by uid %d", path, facts.uid)
	}
	if facts.mode.Perm()&0o022 != 0 {
		return fmt.Errorf("%s is writable by group or others", path)
	}
	return nil
}

func readBounded(path string, limit int64) ([]byte, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	raw, err := io.ReadAll(io.LimitReader(file, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(raw)) > limit {
		return nil, fmt.Errorf("%s exceeds %d bytes", path, limit)
	}
	return raw, nil
}
