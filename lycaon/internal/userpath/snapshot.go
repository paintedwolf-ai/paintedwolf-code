package userpath

import (
	"path/filepath"
	"strings"
)

// Source identifies the resolved PATH origin.
type Source string

// Failure identifies why the login-shell PATH was unavailable.
type Failure string

const (
	// SourceConfigured is an explicit launch value.
	SourceConfigured Source = "configured"
	// SourceProbe is the account login shell.
	SourceProbe Source = "login_shell"
	// SourceInherited is the launch environment.
	SourceInherited Source = "inherited"
	// SourceFallback is the catalog minimum.
	SourceFallback Source = "fallback"
)

const (
	FailureNone             Failure = ""
	FailureShellUnavailable Failure = "shell_unavailable"
	FailureProbeFailed      Failure = "probe_failed"
	FailurePathInvalid      Failure = "path_invalid"
)

// Snapshot is one process-lifetime PATH resolution.
type Snapshot struct {
	entries []string
	source  Source
	failure Failure
	// reason records why a probe did not answer. Empty on success.
	reason string
}

// Entries returns the resolved PATH entries in order.
func (s Snapshot) Entries() []string { return append([]string(nil), s.entries...) }

// Value returns the PATH as a single environment value.
func (s Snapshot) Value() string { return strings.Join(s.entries, string(filepath.ListSeparator)) }

// Source reports where the value came from.
func (s Snapshot) Source() Source { return s.source }

// Failure reports the structured reason a non-probe source was selected.
func (s Snapshot) Failure() Failure { return s.failure }

// Reason explains a non-probe source. Empty when the login shell answered.
func (s Snapshot) Reason() string { return s.reason }

// Contains compares cleaned PATH entries.
func (s Snapshot) Contains(dir string) bool {
	dir = filepath.Clean(strings.TrimSpace(dir))
	if dir == "" {
		return false
	}
	for _, entry := range s.entries {
		if filepath.Clean(entry) == dir {
			return true
		}
	}
	return false
}

// WithAppended adds unique directories after resolved entries.
func (s Snapshot) WithAppended(extra ...string) Snapshot {
	out := s
	out.entries = append([]string(nil), s.entries...)
	for _, dir := range extra {
		dir = filepath.Clean(strings.TrimSpace(dir))
		if dir == "" || dir == "." {
			continue
		}
		if out.Contains(dir) {
			continue
		}
		out.entries = append(out.entries, dir)
	}
	return out
}

// parseEntries keeps unique absolute paths within the configured cap.
func parseEntries(raw string, max int) []string {
	out := make([]string, 0, 8)
	seen := make(map[string]struct{}, 8)
	for _, entry := range filepath.SplitList(raw) {
		if len(out) >= max {
			break
		}
		if !isAcceptableEntry(entry) {
			continue
		}
		clean := filepath.Clean(entry)
		if _, duplicate := seen[clean]; duplicate {
			continue
		}
		seen[clean] = struct{}{}
		out = append(out, clean)
	}
	return out
}

func joinList(entries []string) string {
	return strings.Join(entries, string(filepath.ListSeparator))
}

func isAcceptableEntry(entry string) bool {
	if strings.TrimSpace(entry) == "" {
		return false
	}
	if strings.ContainsAny(entry, "\x00\n\r") {
		return false
	}
	return filepath.IsAbs(entry)
}
