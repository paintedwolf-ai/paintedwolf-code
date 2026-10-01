package hostresources

import (
	"os"
	osexec "os/exec"
	"path/filepath"
	"strings"

	"github.com/lycaon/lycaon/internal/exec"
)

// Resolve executable directories at invocation time so helper lookup follows moved binaries.
func (s *Service) execPathExtra(ids []string, resolved map[string]State) []string {
	byID := s.definitionsByID()
	var out []string
	seen := map[string]struct{}{}
	for _, id := range ids {
		id = strings.TrimSpace(id)
		if _, permitted := resolved[id]; !permitted {
			continue
		}
		def, known := byID[id]
		if !known {
			continue
		}
		realization := realizationForPlatform(def.Realizations, s.env.platform)
		if realization == nil {
			continue
		}
		for _, name := range executableNames(realization.Discover) {
			dir, ok := s.canonicalExecutableDir(name)
			if !ok {
				continue
			}
			if _, duplicate := seen[dir]; duplicate {
				continue
			}
			seen[dir] = struct{}{}
			out = append(out, dir)
			break
		}
	}
	return out
}

// Only fully resolved executable directories enter the child’s PATH.
func (s *Service) canonicalExecutableDir(name string) (string, bool) {
	found, err := s.env.lookPath(name)
	if err != nil {
		return "", false
	}
	resolved, err := filepath.EvalSymlinks(found)
	if err != nil {
		return "", false
	}
	dir := filepath.Dir(resolved)
	info, err := s.env.stat(dir)
	if err != nil || !info.IsDir() {
		return "", false
	}
	return dir, true
}

// executableNames collects the names an expression tree would look up, in order.
func executableNames(expr Expression) []string {
	if expr.Executable != nil {
		return append([]string(nil), expr.Executable.Names...)
	}
	var out []string
	for _, child := range append(append([]Expression(nil), expr.All...), expr.Any...) {
		out = append(out, executableNames(child)...)
	}
	return out
}

func (s *Service) definitionsByID() map[string]Definition {
	s.mu.Lock()
	defer s.mu.Unlock()
	byID := make(map[string]Definition, len(s.defs))
	for _, def := range s.defs {
		byID[def.ID] = def
	}
	return byID
}

// resolvedLookPath searches the resolved PATH children run with, so discovery
// and execution agree on what is installed.
func resolvedLookPath(name string) (string, error) {
	return lookPathIn(filepath.SplitList(exec.EffectivePathValue()), os.Stat)(name)
}

// lookPathIn searches an explicit directory list instead of the process PATH.
func lookPathIn(dirs []string, stat func(string) (os.FileInfo, error)) func(string) (string, error) {
	return func(name string) (string, error) {
		name = strings.TrimSpace(name)
		if name == "" {
			return "", osexec.ErrNotFound
		}
		if strings.ContainsRune(name, filepath.Separator) {
			if executableFile(name, stat) {
				return name, nil
			}
			return "", osexec.ErrNotFound
		}
		for _, dir := range dirs {
			candidate := filepath.Join(dir, name)
			if executableFile(candidate, stat) {
				return candidate, nil
			}
		}
		return "", osexec.ErrNotFound
	}
}

func executableFile(path string, stat func(string) (os.FileInfo, error)) bool {
	info, err := stat(path)
	if err != nil || info.IsDir() {
		return false
	}
	return info.Mode().Perm()&0o111 != 0
}
