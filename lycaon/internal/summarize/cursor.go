package summarize

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"sort"
	"strings"

	"github.com/lycaon/lycaon/internal/pagecursor"
)

type cursorState struct {
	Revision              uint64 `json:"r"`
	Scope                 string `json:"s"`
	Position              string `json:"p"`
	MatchesObserved       int    `json:"m,omitempty"`
	MatchingFilesObserved int    `json:"f,omitempty"`
}

var summarizeCursors = pagecursor.For[cursorState]("summarize")

func encodeCursor(revision uint64, scope, position string) string {
	if revision == 0 || scope == "" || strings.TrimSpace(position) == "" {
		return ""
	}
	token, err := summarizeCursors.Encode(scope, cursorState{Revision: revision, Scope: scope, Position: position})
	if err != nil {
		return ""
	}
	return token
}

func encodePatternCursor(revision uint64, scope, position string, matchesTotal, matchFilesTotal int) string {
	if revision == 0 || scope == "" || strings.TrimSpace(position) == "" {
		return ""
	}
	token, err := summarizeCursors.Encode(scope, cursorState{
		Revision: revision, Scope: scope, Position: position,
		MatchesObserved: matchesTotal, MatchingFilesObserved: matchFilesTotal,
	})
	if err != nil {
		return ""
	}
	return token
}

func cursorScope(req Request) string {
	paths := append([]string(nil), req.Paths...)
	for i := range paths {
		paths[i] = strings.TrimSpace(paths[i])
	}
	sort.Strings(paths)
	raw, err := json.Marshal(struct {
		Task    string   `json:"task"`
		Path    string   `json:"path"`
		Paths   []string `json:"paths"`
		Pattern string   `json:"pattern"`
	}{Task: req.Task, Path: strings.TrimSpace(req.Path), Paths: paths, Pattern: req.Pattern})
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(raw)
	return base64.RawURLEncoding.EncodeToString(sum[:16])
}
