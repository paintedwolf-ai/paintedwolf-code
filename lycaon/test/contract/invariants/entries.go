package invariants

import (
	"fmt"
	"io/fs"
	"os/exec"
	"path/filepath"
	"strings"

	lyexec "github.com/lycaon/lycaon/internal/exec"
)

// Entry is one file system entry below a directory, repository-relative.
type Entry struct {
	Rel  string
	Mode fs.FileMode
}

// Entries lists every entry below a repository-relative directory, symlinks
// and nested .git included, in walk order. A walk error or an empty
// directory ends the check.
func (c *Corpus) Entries(dir string) []Entry {
	root := c.Path(dir)
	var out []Entry
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if path == root {
			return nil
		}
		rel, err := filepath.Rel(c.root, path)
		if err != nil {
			return err
		}
		out = append(out, Entry{Rel: filepath.ToSlash(rel), Mode: d.Type()})
		return nil
	})
	if err != nil {
		panic(abort{fmt.Errorf("walk %s: %w", dir, err)})
	}
	if len(out) == 0 {
		panic(abort{fmt.Errorf("empty selection: nothing below %s", dir)})
	}
	return out
}

// GitEntry is one path git tracks, with its index mode.
type GitEntry struct {
	Rel  string
	Mode string
}

// GitFiles lists what git tracks below a repository-relative directory. A
// missing git, a checkout git cannot read, or an empty listing ends the
// check.
func (c *Corpus) GitFiles(dir string) []GitEntry {
	cmd := exec.Command("git", "ls-files", "-s", "--", dir)
	cmd.Dir = c.root
	cmd.Env = lyexec.LocalGitEnv()
	out, err := cmd.Output()
	if err != nil {
		panic(abort{fmt.Errorf("git ls-files %s: %w", dir, err)})
	}
	var entries []GitEntry
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		meta, path, ok := strings.Cut(line, "\t")
		fields := strings.Fields(meta)
		if !ok || len(fields) < 3 {
			continue
		}
		entries = append(entries, GitEntry{Rel: path, Mode: fields[0]})
	}
	if len(entries) == 0 {
		panic(abort{fmt.Errorf("empty selection: git tracks nothing below %s", dir)})
	}
	return entries
}
