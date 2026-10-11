package evidence

import (
	"net/url"
	"path/filepath"
	"strings"
)

var knownCitationExts = map[string]struct{}{
	".go": {}, ".py": {}, ".ts": {}, ".tsx": {}, ".js": {}, ".jsx": {},
	".md": {}, ".yaml": {}, ".yml": {}, ".json": {}, ".sql": {},
	".rs": {}, ".java": {}, ".kt": {}, ".rb": {}, ".php": {},
	".c": {}, ".h": {}, ".cpp": {}, ".hpp": {}, ".cs": {},
	".sh": {}, ".bash": {}, ".zsh": {},
}

func looksLikeShellPathToken(token string) bool {
	token = strings.TrimSpace(strings.Trim(token, `"'`))
	if token == "" {
		return false
	}
	if strings.ContainsAny(token, " \t\n\r\f\v$()<>|;&`\\") {
		return false
	}
	// Reject JSON debris from tokenizing structured command tool bodies.
	if strings.ContainsAny(token, `"'{},`) {
		return false
	}
	if strings.HasPrefix(token, "-") {
		return false
	}
	if parsed, err := url.Parse(token); err == nil && parsed.Scheme != "" && !filepath.IsAbs(token) {
		return false
	}
	if strings.Contains(token, "/") {
		return true
	}
	ext := strings.ToLower(filepath.Ext(token))
	if ext == "" {
		return false
	}
	_, ok := knownCitationExts[ext]
	return ok
}
