package projectsource

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/textfile"
)

func TestSourceReadUTF8AcrossSniffBoundary(t *testing.T) {
	t.Parallel()
	for _, runeText := range []string{"é", "界", "🐺"} {
		for split := 1; split < len(runeText); split++ {
			for _, oversized := range []bool{false, true} {
				root := t.TempDir()
				content := strings.Repeat("a", sourceSniffPrefixBytes-split) + runeText + "\n"
				if oversized {
					content += strings.Repeat("b", SourceReadMaxBytes)
				}
				testutil.FailErr(t, "write UTF-8 fixture", os.WriteFile(filepath.Join(root, "unicode.txt"), []byte(content), 0o600))
				p := &Project{ID: "p", Roots: []Root{{ID: "r", Path: root, IsPrimary: true}}}
				got, err := ReadProjectSource(p, SourceReadRequest{Path: "unicode.txt"})
				testutil.FailErr(t, "read UTF-8 across sniff boundary", err)
				if got.Binary || got.OverLimit != oversized || got.Encoding != textfile.UTF8 {
					t.Fatalf("rune=%q split=%d oversized=%v: binary=%v over_limit=%v encoding=%q", runeText, split, oversized, got.Binary, got.OverLimit, got.Encoding)
				}
				if !oversized && got.Content != content {
					t.Fatalf("rune=%q split=%d: content changed", runeText, split)
				}
			}
		}
	}
}

func TestSourceReadDoesNotRepairMalformedUTF8(t *testing.T) {
	t.Parallel()
	for _, suffix := range []string{"\xc3", "\xc3x", "\xff\n"} {
		root := t.TempDir()
		content := strings.Repeat("a", sourceSniffPrefixBytes-1) + suffix
		testutil.FailErr(t, "write malformed fixture", os.WriteFile(filepath.Join(root, "bad.txt"), []byte(content), 0o600))
		p := &Project{ID: "p", Roots: []Root{{ID: "r", Path: root, IsPrimary: true}}}
		_, err := ReadProjectSource(p, SourceReadRequest{Path: "bad.txt"})
		if !errors.Is(err, ErrSourceUnsupportedEncoding) {
			t.Fatalf("suffix=%q: error=%v, want unsupported encoding", suffix, err)
		}
	}
}
