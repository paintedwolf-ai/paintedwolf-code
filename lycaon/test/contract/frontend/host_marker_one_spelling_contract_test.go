package contract

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/hostmarker"
	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

// guardedMarkers are the host-written literals distinctive enough to scan for.
// The rest of hostmarker.All (">>> ", "]", "Code:") are too short to match
// without false positives; they reach Den through the same generated module.
var guardedMarkers = []string{
	hostmarker.VerbatimHeadTail,
	hostmarker.CompactionBannerOpen,
	hostmarker.Rejected,
}

// A marker is the host reading back its own writing, true only while both sides
// spell it the same way. Go reads internal/hostmarker; Den reads the generated
// module. A second literal elsewhere breaks a reader with nothing failing.
//
// The scan anchors on what precedes a marker rather than on quotes, so it sees
// markers inside larger literals and regexes but not struct fields or properties
// ending in `Rejected:`. Comments are scanned too.
func TestHostMarkersHaveOneSpelling(t *testing.T) {
	t.Parallel()
	root := contractcheck.RepoRoot(t)

	var hits []string
	hits = append(hits, scanForMarkers(t, filepath.Join(root, "lycaon", "internal"),
		func(rel string) bool {
			return strings.HasPrefix(rel, "hostmarker"+string(filepath.Separator))
		}, ".go")...)
	hits = append(hits, scanForMarkers(t, filepath.Join(root, "lycaon-den", "src"),
		func(rel string) bool {
			return strings.HasSuffix(rel, "host-markers.generated.ts")
		}, ".ts", ".tsx")...)

	sort.Strings(hits)
	contractcheck.FailViolations(t, `host markers spelled outside their definition

Go reads and writes these through internal/hostmarker. Den reads
src/chat/host-markers.generated.ts, produced by ./task codegen:host-markers.
A literal elsewhere keeps matching text nobody writes once the constant is
renamed, and nothing fails.`, hits)
}

// scanForMarkers reports guarded markers in dir, skipping tests, fixtures, and
// the definition itself.
func scanForMarkers(t *testing.T, dir string, isDefinition func(rel string) bool, exts ...string) []string {
	t.Helper()
	var hits []string
	err := filepath.WalkDir(dir, func(path string, d os.DirEntry, walkErr error) error {
		if walkErr != nil || d.IsDir() {
			return walkErr
		}
		rel, relErr := filepath.Rel(dir, path)
		if relErr != nil {
			return relErr
		}
		if !hasAnySuffix(rel, exts) || isTestOrFixtureFile(rel) || isDefinition(rel) {
			return nil
		}
		// #nosec G304 -- paths come from walking the repository.
		raw, readErr := os.ReadFile(path)
		if readErr != nil {
			return readErr
		}
		for _, marker := range guardedMarkers {
			if !spellsMarker(string(raw), marker) {
				continue
			}
			hits = append(hits, rel+": "+marker)
		}
		return nil
	})
	contractcheck.FailErr(t, "walk "+dir, err)
	return hits
}

// markerBoundaries precede a marker being spelled as text: a string quote, or a
// regex start anchor. Anything else in front — an identifier character, a space,
// a brace — makes it a field or property name.
const markerBoundaries = "\"`'^"

func spellsMarker(body, marker string) bool {
	for offset := 0; ; {
		idx := strings.Index(body[offset:], marker)
		if idx < 0 {
			return false
		}
		at := offset + idx
		offset = at + 1
		if at == 0 {
			return true
		}
		if strings.ContainsRune(markerBoundaries, rune(body[at-1])) {
			return true
		}
		// A `\n` escape immediately before the marker, inside one literal.
		if at >= 2 && body[at-1] == 'n' && body[at-2] == '\\' {
			return true
		}
	}
}

func hasAnySuffix(name string, suffixes []string) bool {
	for _, suffix := range suffixes {
		if strings.HasSuffix(name, suffix) {
			return true
		}
	}
	return false
}

// isTestOrFixtureFile skips files whose job is to hold example transcript text.
func isTestOrFixtureFile(rel string) bool {
	base := filepath.Base(rel)
	switch {
	case strings.HasSuffix(base, "_test.go"),
		strings.Contains(base, ".test."),
		strings.Contains(base, "-fixtures."),
		strings.Contains(rel, "testdata"+string(filepath.Separator)):
		return true
	}
	return false
}
