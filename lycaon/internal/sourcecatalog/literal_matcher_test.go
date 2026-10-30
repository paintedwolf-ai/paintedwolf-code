package sourcecatalog

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/litprefilter"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/textsearch"
)

func TestByteFiltersNeverExcludeMatcherResults(t *testing.T) {
	root := t.TempDir()
	markLiteralWatch(t, root)
	index := New().Literals
	contents := []string{"NeedleToken", "ébc", "Kelvin ſource Σigma", "İstanbul ıSTANBUL", "ÉLAN élan", "abc abbc abcd", "start\nend", "", "\xffNeedleToken", "\ufeffNeedleToken", "\x00NeedleToken", "\xff\xfeN\x00e\x00e\x00d\x00l\x00e\x00"}
	entries := make([]Entry, len(contents))
	for i, content := range contents {
		rel := fmt.Sprintf("file-%02d", i)
		writeLiteralFixture(t, root, rel, content)
		info, err := os.Stat(filepath.Join(root, rel))
		testutil.FailErr(t, "stat content", err)
		entries[i] = Entry{Path: rel, Size: info.Size(), Mode: uint32(info.Mode()), Modified: info.ModTime()}
		index.BeginRead(root, entries[i], fileVersion(root, entries[i])).Observe(t.Context(), []byte(content))
	}
	limits, err := textsearch.CatalogLimits()
	testutil.FailErr(t, "matcher limits", err)
	for _, dialect := range []textsearch.Dialect{textsearch.Fixed, textsearch.Basic, textsearch.Extended, textsearch.RE2, textsearch.Perl} {
		patterns := []string{"needletoken", "kelvin", "source", "σigma", "istanbul", "ÉLAN", "abc", "a.*bc", "a(b|bb)c", "a?bc", "^$", "^start", "end$", ""}
		if dialect == textsearch.RE2 {
			patterns = append(patterns, "(?i:needle)Token", `\bNeedleToken\b`)
		}
		if dialect == textsearch.Basic {
			patterns = append(patterns, `\(abc\)\1`, `a\(b\|bb\)c`)
		}
		if dialect == textsearch.Fixed {
			patterns = append(patterns, "\xa9bc")
		}
		for _, pattern := range patterns {
			for _, fold := range []bool{false, true} {
				for _, word := range []bool{false, true} {
					if pattern == "\xa9bc" && (fold || word) {
						continue
					}
					spec := textsearch.Spec{Dialect: dialect, Patterns: []string{pattern}, IgnoreCase: fold, Word: word}
					matcher, compileErr := textsearch.Compile(t.Context(), spec, limits)
					testutil.FailErr(t, fmt.Sprintf("compile dialect=%v pattern=%q fold=%v word=%v", dialect, pattern, fold, word), compileErr)
					for i, content := range contents {
						_, matched, matchErr := matcher.Select(t.Context(), []byte(content))
						testutil.FailErr(t, "match corpus", matchErr)
						if matched && index.CanPruneBytes(root, matcher.Requirement(), entries[i], fileVersion(root, entries[i])) {
							t.Fatalf("filter excluded dialect=%v pattern=%q fold=%v word=%v content=%q", dialect, pattern, fold, word, content)
						}
					}
				}
			}
		}
	}
}

func TestCanonicalByteFiltersPreserveEveryByteSubstring(t *testing.T) {
	const content = "ébc Kelvin ſource Σigma"
	root, entry, index := captureFixture(t, content)
	index.BeginRead(root, entry, fileVersion(root, entry)).Observe(t.Context(), []byte(content))
	limits, err := textsearch.CatalogLimits()
	testutil.FailErr(t, "matcher limits", err)
	for start := 0; start < len(content); start++ {
		for end := start + 1; end <= len(content); end++ {
			pattern := content[start:end]
			for _, patterns := range [][]string{{pattern}, {"unrelated-pattern", pattern}} {
				matcher, err := textsearch.Compile(t.Context(), textsearch.Spec{Dialect: textsearch.Fixed, Patterns: patterns}, limits)
				testutil.FailErr(t, "compile byte patterns", err)
				_, matched, err := matcher.Select(t.Context(), []byte(content))
				testutil.FailErr(t, "match byte substring", err)
				if !matched || index.CanPruneBytes(root, matcher.Requirement(), entry, fileVersion(root, entry)) || index.CanPruneBytes(root, litprefilter.AnyOf(patterns...), entry, fileVersion(root, entry)) {
					t.Fatalf("excluded byte interval %d:%d patterns=%q", start, end, patterns)
				}
			}
		}
	}
}
