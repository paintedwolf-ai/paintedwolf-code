package gitrepo

import (
	"bufio"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
)

// maxAlternateDepth matches Git's own limit on chained alternates.
const maxAlternateDepth = 5

// RepositoryDirs are the directories Git uses when it opens path as a
// repository, symlinks resolved: own holds its git directory, the common
// directory a linked worktree shares and the object store; shared holds every
// alternate object store, which Git only reads. ok is false when path is
// neither a work tree nor a bare repository.
func RepositoryDirs(path string) (own, shared []string, ok bool) {
	path = filepath.Clean(path)
	gitDir := ""
	dot := filepath.Join(path, dotGit)
	// Git follows a .git symlink, to a directory or to a gitfile.
	switch info, err := os.Stat(dot); {
	case err == nil && info.Mode().IsRegular():
		gitDir = readGitDirPointer(path, dot)
	case err == nil && info.IsDir():
		gitDir = dot
	case isBareRepository(path):
		gitDir = path
	}
	if gitDir == "" {
		return nil, nil, false
	}
	commonDir := readCommonDir(gitDir)
	if commonDir == "" {
		return nil, nil, false
	}
	add := func(list *[]string, dir string) {
		if resolved, err := filepath.EvalSymlinks(dir); err == nil && !slices.Contains(own, resolved) && !slices.Contains(shared, resolved) {
			*list = append(*list, resolved)
		}
	}
	objects := filepath.Join(commonDir, "objects")
	for _, dir := range []string{gitDir, commonDir, objects} {
		add(&own, dir)
	}
	for _, alternate := range alternates(objects, 0) {
		add(&shared, alternate)
	}
	return own, shared, len(own) > 0
}

func isBareRepository(path string) bool {
	for _, name := range []string{"HEAD", "objects", "refs"} {
		if _, err := os.Stat(filepath.Join(path, name)); err != nil {
			return false
		}
	}
	return true
}

// alternates lists the object stores objects/info/alternates names, each
// relative to the object store that names it, and theirs in turn.
func alternates(objects string, depth int) []string {
	if depth >= maxAlternateDepth {
		return nil
	}
	// #nosec G304 -- the alternates file is fixed inside the object store.
	file, err := os.Open(filepath.Join(objects, "info", "alternates"))
	if err != nil {
		return nil
	}
	defer func() { _ = file.Close() }()
	var out []string
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		// Git C-quotes an alternate whose path needs it.
		if strings.HasPrefix(line, `"`) {
			unquoted, err := strconv.Unquote(line)
			if err != nil {
				continue
			}
			line = unquoted
		}
		alternate := resolveAgainst(objects, line)
		out = append(out, alternate)
		out = append(out, alternates(alternate, depth+1)...)
	}
	return out
}
