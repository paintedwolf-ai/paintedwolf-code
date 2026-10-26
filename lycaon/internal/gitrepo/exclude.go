package gitrepo

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

const recoveryExcludePattern = ".paintedwolf/recovery/"

// EnsureRecoveryExcluded records .paintedwolf/recovery/ in the repository's
// .git/info/exclude when dir belongs to a Git repository. It never edits .gitignore.
func EnsureRecoveryExcluded(dir string) error {
	repo, ok := Discover(dir)
	if !ok || repo.CommonDir == "" {
		return nil
	}
	if err := ensureExcludePattern(filepath.Join(repo.CommonDir, "info", "exclude"), recoveryExcludePattern); err != nil {
		return err
	}
	if repo.GitDir != "" && repo.GitDir != repo.CommonDir {
		_ = ensureExcludePattern(filepath.Join(repo.GitDir, "info", "exclude"), recoveryExcludePattern)
	}
	return nil
}

func ensureExcludePattern(excludeFile, pattern string) error {
	// #nosec G304 -- excludeFile is <CommonDir>/info/exclude or <GitDir>/info/exclude.
	raw, err := os.ReadFile(excludeFile)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	trimmed := strings.TrimSuffix(pattern, "/")
	for _, line := range strings.Split(string(raw), "\n") {
		entry := strings.TrimSpace(line)
		if entry == pattern || entry == trimmed || entry == "/"+pattern || entry == "/"+trimmed {
			return nil
		}
	}
	if err := os.MkdirAll(filepath.Dir(excludeFile), 0o755); err != nil {
		return err
	}
	var out strings.Builder
	out.Write(raw)
	if len(raw) > 0 && raw[len(raw)-1] != '\n' {
		out.WriteByte('\n')
	}
	out.WriteString(pattern)
	out.WriteByte('\n')
	return os.WriteFile(excludeFile, []byte(out.String()), 0o644)
}
