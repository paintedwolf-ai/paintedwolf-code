package sandbox

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/lycaon/lycaon/pkg/pathglob"
)

// EntryGlob is a validated discovery filter. Brace alternatives belong to
// discovery syntax; permission-profile globs retain their own grammar.
type EntryGlob struct {
	patterns []string
}

// CompileEntryGlob validates discovery syntax before a tree is searched.
func CompileEntryGlob(pattern string) (EntryGlob, error) {
	pattern = filepath.ToSlash(strings.TrimSpace(pattern))
	if len(pattern) > 4096 {
		return EntryGlob{}, fmt.Errorf("glob exceeds 4096 bytes")
	}
	patterns, err := expandEntryBraces(pattern)
	if err != nil {
		return EntryGlob{}, err
	}
	for _, expanded := range patterns {
		for _, segment := range strings.Split(expanded, "/") {
			if _, err := filepath.Match(segment, ""); err != nil {
				return EntryGlob{}, err
			}
		}
	}
	return EntryGlob{patterns: patterns}, nil
}

// Match applies a bare pattern to the leaf and a path pattern to the whole path.
func (g EntryGlob) Match(relPath string) bool {
	if len(g.patterns) == 0 {
		return true
	}
	relPath = filepath.ToSlash(strings.TrimSpace(relPath))
	for _, pattern := range g.patterns {
		candidate := relPath
		if !strings.Contains(pattern, "/") && !strings.Contains(pattern, "**") {
			candidate = filepath.Base(candidate)
		}
		if pattern == "" || pathglob.Match(pattern, candidate) {
			return true
		}
	}
	return false
}

func expandEntryBraces(pattern string) ([]string, error) {
	patterns := []string{pattern}
	for i := 0; i < len(patterns); {
		p := patterns[i]
		start, end, commas, err := entryBraceGroup(p)
		if err != nil {
			return nil, err
		}
		if start < 0 {
			i++
			continue
		}
		if len(commas) == 0 {
			return nil, fmt.Errorf("brace group requires comma-separated alternatives; escape literal braces")
		}
		if len(patterns)+len(commas) > 256 {
			return nil, fmt.Errorf("glob exceeds 256 alternatives")
		}
		var replacements []string
		from := start + 1
		for _, to := range append(commas, end) {
			replacements = append(replacements, p[:start]+p[from:to]+p[end+1:])
			from = to + 1
		}
		patterns = append(append(patterns[:i:i], replacements...), patterns[i+1:]...)
	}
	return patterns, nil
}

func entryBraceGroup(pattern string) (start, end int, commas []int, err error) {
	start, end = -1, -1
	depth := 0
	class := false
	for i := 0; i < len(pattern); i++ {
		switch pattern[i] {
		case '\\':
			i++
		case '[':
			class = true
		case ']':
			class = false
		case '{':
			if !class {
				if depth == 0 {
					start = i
				}
				depth++
			}
		case ',':
			if !class && depth == 1 {
				commas = append(commas, i)
			}
		case '}':
			if class {
				continue
			}
			depth--
			if depth < 0 {
				return -1, -1, nil, fmt.Errorf("unmatched closing brace")
			}
			if depth == 0 {
				return start, i, commas, nil
			}
		}
	}
	if depth != 0 {
		return -1, -1, nil, fmt.Errorf("unclosed brace group")
	}
	return start, end, commas, nil
}
