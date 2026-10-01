package summarize

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"sort"
	"strings"
)

const cursorVersion = "v1"

type cursorState struct {
	Revision              uint64 `json:"r"`
	Scope                 string `json:"s"`
	Position              string `json:"p"`
	MatchesObserved       int    `json:"m,omitempty"`
	MatchingFilesObserved int    `json:"f,omitempty"`
}

var cursorMACKey = func() []byte {
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		panic(err)
	}
	return key
}()

func encodeCursor(revision uint64, scope, position string) string {
	if revision == 0 || scope == "" || strings.TrimSpace(position) == "" {
		return ""
	}
	raw, err := json.Marshal(cursorState{Revision: revision, Scope: scope, Position: position})
	if err != nil {
		return ""
	}
	return sealCursor(raw)
}

func encodePatternCursor(revision uint64, scope, position string, matchesTotal, matchFilesTotal int) string {
	if revision == 0 || scope == "" || strings.TrimSpace(position) == "" {
		return ""
	}
	raw, err := json.Marshal(cursorState{
		Revision: revision, Scope: scope, Position: position,
		MatchesObserved: matchesTotal, MatchingFilesObserved: matchFilesTotal,
	})
	if err != nil {
		return ""
	}
	return sealCursor(raw)
}

func sealCursor(raw []byte) string {
	payload := base64.RawURLEncoding.EncodeToString(raw)
	mac := hmac.New(sha256.New, cursorMACKey)
	_, _ = mac.Write([]byte(payload))
	signature := base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
	return cursorVersion + "." + payload + "." + signature
}

func decodeCursor(cursor string) (cursorState, bool) {
	cursor = strings.TrimSpace(cursor)
	parts := strings.Split(cursor, ".")
	if len(parts) != 3 || parts[0] != cursorVersion {
		return cursorState{}, false
	}
	mac := hmac.New(sha256.New, cursorMACKey)
	_, _ = mac.Write([]byte(parts[1]))
	want := mac.Sum(nil)
	got, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil || !hmac.Equal(got, want) {
		return cursorState{}, false
	}
	raw, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return cursorState{}, false
	}
	var state cursorState
	if err := json.Unmarshal(raw, &state); err != nil || state.Revision == 0 || state.Scope == "" || strings.TrimSpace(state.Position) == "" {
		return cursorState{}, false
	}
	return state, true
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
