package sourcescope

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/go-git/go-git/v5/plumbing/format/gitignore"
)

// Ignore rules apply even outside a repository.
const (
	gitIgnoreName = ".gitignore"
	plainIgnore   = ".ignore"
)

var repoLocalExclude = filepath.Join(".git", "info", "exclude")

// Parsed rules are shared by content and root-relative directory.
const ignoreFileCacheLimit = 8192

// Per-scope caches retain only recently used ancestor rules.
const scopeIgnoreDirectoryLimit = 1024

type ignoreCacheKey struct {
	digest [sha256.Size]byte
	domain string
}

var ignoreFiles = struct {
	mu    sync.Mutex
	files map[ignoreCacheKey][]gitignore.Pattern
}{files: make(map[ignoreCacheKey][]gitignore.Pattern)}

// Unreadable or non-regular ignore files contribute no patterns.
func ignorePatterns(abs string, domain []string) []gitignore.Pattern {
	info, err := os.Lstat(abs)
	if err != nil || !info.Mode().IsRegular() {
		return nil
	}
	data, err := os.ReadFile(abs) // #nosec G304 -- ignore file inside the observed root
	if err != nil {
		return nil
	}
	key := ignoreCacheKey{digest: sha256.Sum256(data), domain: strings.Join(domain, "/")}
	ignoreFiles.mu.Lock()
	cached, ok := ignoreFiles.files[key]
	ignoreFiles.mu.Unlock()
	if ok {
		return cached
	}
	patterns := parseIgnore(data, domain)
	ignoreFiles.mu.Lock()
	if len(ignoreFiles.files) >= ignoreFileCacheLimit {
		clear(ignoreFiles.files)
	}
	ignoreFiles.files[key] = patterns
	ignoreFiles.mu.Unlock()
	return patterns
}

func parseIgnore(data []byte, domain []string) []gitignore.Pattern {
	var out []gitignore.Pattern
	scanner := bufio.NewScanner(bytes.NewReader(data))
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for scanner.Scan() {
		line := scanner.Text()
		if strings.TrimSpace(line) == "" || strings.HasPrefix(line, "#") {
			continue
		}
		out = append(out, gitignore.ParsePattern(line, domain))
	}
	return out
}

func directoryPatterns(rootAbs, relDir string) []gitignore.Pattern {
	dirAbs := rootAbs
	var domain []string
	if relDir != "." && relDir != "" {
		dirAbs = filepath.Join(rootAbs, filepath.FromSlash(relDir))
		domain = strings.Split(relDir, "/")
	}
	var out []gitignore.Pattern
	if relDir == "." || relDir == "" {
		out = append(out, ignorePatterns(filepath.Join(rootAbs, repoLocalExclude), nil)...)
	}
	out = append(out, ignorePatterns(filepath.Join(dirAbs, gitIgnoreName), domain)...)
	out = append(out, ignorePatterns(filepath.Join(dirAbs, plainIgnore), domain)...)
	return out
}

// Deeper files and later patterns take precedence.
func lastMatch(stack [][]gitignore.Pattern, segments []string, isDir bool) gitignore.MatchResult {
	for i := len(stack) - 1; i >= 0; i-- {
		file := stack[i]
		for j := len(file) - 1; j >= 0; j-- {
			if result := file[j].Match(segments, isDir); result != gitignore.NoMatch {
				return result
			}
		}
	}
	return gitignore.NoMatch
}
