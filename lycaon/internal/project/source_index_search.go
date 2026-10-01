package project

import (
	"container/heap"
	"context"
	"errors"
	"path/filepath"
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/lycaon/lycaon/internal/sourcecatalog"
)

// SourceIndexMatch is one ranked file and the parts of its path the query matched.
type SourceIndexMatch struct {
	SourceIndexEntry
	// Highlights are ascending, disjoint code point ranges into Path.
	Highlights []SourceTextRange
}

// SourceTextRange is a half-open range of code point offsets.
type SourceTextRange struct{ Start, End int }

// sourcePathReading is one way a query names files. The literal reading matches every root's
// relative paths. An anchored reading lets the tail of one root folder's own path absorb the
// query up to a separator, so any portion of a file's full path finds it.
type sourcePathReading struct {
	rootID string
	rest   string
	// anchor counts the query code points the root's path absorbed, separator included.
	anchor int
}

func (r sourcePathReading) anchored() bool { return r.rootID != "" }

type rankedSourceFile struct {
	entry       SourceIndexEntry
	tier, score int
	reading     int
}

type sourceFileHeap []rankedSourceFile

func (h sourceFileHeap) Len() int           { return len(h) }
func (h sourceFileHeap) Less(i, j int) bool { return compareSourceFile(h[i], h[j]) > 0 }
func (h sourceFileHeap) Swap(i, j int)      { h[i], h[j] = h[j], h[i] }
func (h *sourceFileHeap) Push(v any)        { *h = append(*h, v.(rankedSourceFile)) }
func (h *sourceFileHeap) Pop() any {
	old := *h
	item := old[len(old)-1]
	*h = old[:len(old)-1]
	return item
}

// SearchSourceIndex ranks bounded pages and resumes by address within the pinned generation.
// A path that matches nothing is retried without its leading directories, so a location copied
// from another checkout, a CI runner, or a module path still finds the local file.
func SearchSourceIndex(ctx context.Context, snapshot SourceIndexSnapshot, query SourceQuery, style SourcePathStyle, rootID string, after *SourceIndexEntry, limit int) ([]SourceIndexMatch, error) {
	if limit <= 0 || limit > 201 {
		return nil, errors.New("source search page limit is outside its bounds")
	}
	q := strings.ToLower(strings.TrimSpace(query.Path))
	if query.Remote != nil {
		q = snapshot.remoteFileQuery(ctx, *query.Remote, style)
	}
	if q == "" {
		return browseSourceIndex(ctx, snapshot, rootID, after, limit)
	}
	var roots []sourceIndexReader
	for _, root := range snapshot.readers {
		if rootID == "" || root.rootID == rootID {
			roots = append(roots, root)
		}
	}
	paths := sourceRootPaths(roots, style)
	for {
		matches, err := searchSourcePath(ctx, roots, paths, q, after, limit)
		if err != nil || len(matches) > 0 {
			return matches, err
		}
		_, rest, relaxable := strings.Cut(strings.TrimLeft(q, "/"), "/")
		if !relaxable || rest == "" {
			return matches, nil
		}
		// A later page that came back empty ended this level's results.
		if after != nil {
			if first, err := searchSourcePath(ctx, roots, paths, q, nil, 1); err != nil || len(first) > 0 {
				return nil, err
			}
		}
		q = rest
	}
}

func searchSourcePath(ctx context.Context, roots []sourceIndexReader, rootPaths map[string][]string, q string, after *SourceIndexEntry, limit int) ([]SourceIndexMatch, error) {
	search := &sourceIndexSearch{readings: sourcePathReadings(q, rootPaths), limit: limit, after: after}
	if after != nil {
		search.position, _ = rankSourceEntry(*after, search.readings)
	}
	literalTiers := []sourcecatalog.FilePathMatch{sourcecatalog.FileBasenamePrefix, sourcecatalog.FileBasenameContains, sourcecatalog.FilePathSubsequence}
	for tier, match := range literalTiers {
		for _, root := range roots {
			if err := search.scan(ctx, root, q, match, 0); err != nil {
				return nil, err
			}
		}
		// Anchored readings rank with literal path subsequences.
		if tier == len(literalTiers)-1 {
			for index, reading := range search.readings[1:] {
				if err := search.scanAnchored(ctx, roots, reading, index+1); err != nil {
					return nil, err
				}
			}
		}
		if search.best.Len() == limit {
			break
		}
	}
	slices.SortFunc(search.best, compareSourceFile)
	out := make([]SourceIndexMatch, len(search.best))
	for i, item := range search.best {
		out[i] = SourceIndexMatch{SourceIndexEntry: item.entry, Highlights: sourceHighlights(item, search.readings[item.reading])}
	}
	return out, nil
}

type sourceIndexSearch struct {
	readings []sourcePathReading
	best     sourceFileHeap
	limit    int
	after    *SourceIndexEntry
	position rankedSourceFile
}

// offer keeps entry when reading is its best, so each file enters the heap once across scans.
func (s *sourceIndexSearch) offer(entry SourceIndexEntry, reading int) {
	item, ok := rankSourceEntry(entry, s.readings)
	if !ok || item.reading != reading || (s.after != nil && compareSourceFile(item, s.position) <= 0) {
		return
	}
	if s.best.Len() < s.limit {
		heap.Push(&s.best, item)
	} else if compareSourceFile(item, s.best[0]) < 0 {
		s.best[0] = item
		heap.Fix(&s.best, 0)
	}
}

func (s *sourceIndexSearch) scan(ctx context.Context, root sourceIndexReader, query string, match sourcecatalog.FilePathMatch, reading int) error {
	// A basename never holds a separator.
	if match != sourcecatalog.FilePathSubsequence && strings.Contains(query, "/") {
		return nil
	}
	after := ""
	for {
		paths, err := root.reader.MatchingFilePathsPage(ctx, sourceFileScope, query, match, after, sourcecatalog.TreeFilePageLimit)
		if err != nil || len(paths) == 0 {
			return err
		}
		after = paths[len(paths)-1]
		for _, path := range paths {
			s.offer(SourceIndexEntry{RootID: root.rootID, Path: path}, reading)
		}
	}
}

func (s *sourceIndexSearch) scanAnchored(ctx context.Context, roots []sourceIndexReader, reading sourcePathReading, index int) error {
	for _, root := range roots {
		if root.rootID != reading.rootID {
			continue
		}
		if reading.rest == "" {
			return s.browseAnchored(ctx, root, reading, index)
		}
		for _, match := range []sourcecatalog.FilePathMatch{sourcecatalog.FileBasenamePrefix, sourcecatalog.FileBasenameContains, sourcecatalog.FilePathSubsequence} {
			if err := s.scan(ctx, root, reading.rest, match, index); err != nil {
				return err
			}
		}
	}
	return nil
}

// browseAnchored lists a root whose own path absorbed the whole query. Every file ranks
// equally, so address order ends the scan once the heap holds better files.
func (s *sourceIndexSearch) browseAnchored(ctx context.Context, root sourceIndexReader, reading sourcePathReading, index int) error {
	length, after := 0, ""
	for {
		paths, err := root.reader.FileAddressPage(ctx, sourceFileScope, length, after, sourcecatalog.TreeFilePageLimit)
		if err != nil || len(paths) == 0 {
			return err
		}
		for _, path := range paths {
			entry := SourceIndexEntry{RootID: root.rootID, Path: path}
			own := rankedSourceFile{entry: entry, tier: 2, score: reading.anchor, reading: index}
			if s.best.Len() == s.limit && compareSourceFile(own, s.best[0]) >= 0 {
				return nil
			}
			s.offer(entry, index)
		}
		after = paths[len(paths)-1]
		length = len(after)
	}
}

func browseSourceIndex(ctx context.Context, snapshot SourceIndexSnapshot, rootID string, after *SourceIndexEntry, limit int) ([]SourceIndexMatch, error) {
	var best []rankedSourceFile
	for _, root := range snapshot.readers {
		if rootID != "" && root.rootID != rootID {
			continue
		}
		length, path := 0, ""
		if after != nil {
			length, path = len(after.Path), after.Path
			if root.rootID < after.RootID {
				length++
				path = ""
			}
			if root.rootID > after.RootID {
				path = ""
			}
		}
		paths, err := root.reader.FileAddressPage(ctx, sourceFileScope, length, path, limit)
		if err != nil {
			return nil, err
		}
		for _, path := range paths {
			best = append(best, rankedSourceFile{entry: SourceIndexEntry{RootID: root.rootID, Path: path}})
		}
		slices.SortFunc(best, compareSourceFile)
		best = best[:min(limit, len(best))]
	}
	out := make([]SourceIndexMatch, len(best))
	for i, item := range best {
		out[i] = SourceIndexMatch{SourceIndexEntry: item.entry}
	}
	return out, nil
}

// sourceRootPaths lists each root's own path as queries compare it: as attached, and with
// links resolved, since a pasted path may come from either spelling.
func sourceRootPaths(roots []sourceIndexReader, style SourcePathStyle) map[string][]string {
	out := make(map[string][]string, len(roots))
	for _, root := range roots {
		spellings := []string{root.path}
		if resolved, err := filepath.EvalSymlinks(root.path); err == nil && resolved != root.path {
			spellings = append(spellings, resolved)
		}
		for _, spelling := range spellings {
			path := strings.TrimRight(style.normalize(spelling), "/")
			if !slices.Contains(out[root.rootID], path) {
				out[root.rootID] = append(out[root.rootID], path)
			}
		}
	}
	return out
}

// sourcePathReadings returns the literal reading first, then one anchored reading for each
// separator in query whose prefix ends a root's path.
func sourcePathReadings(query string, roots map[string][]string) []sourcePathReading {
	readings := []sourcePathReading{{rest: query}}
	ids := make([]string, 0, len(roots))
	for id := range roots {
		ids = append(ids, id)
	}
	slices.Sort(ids)
	for _, id := range ids {
		for i := 0; i < len(query); i++ {
			if query[i] != '/' || !slices.ContainsFunc(roots[id], func(path string) bool { return strings.HasSuffix(path, query[:i]) }) {
				continue
			}
			reading := sourcePathReading{rootID: id, rest: query[i+1:], anchor: utf8.RuneCountInString(query[:i+1])}
			if !slices.ContainsFunc(readings, func(r sourcePathReading) bool { return r.rootID == id && r.rest == reading.rest }) {
				readings = append(readings, reading)
			}
		}
	}
	return readings
}

// rankSourceEntry ranks entry by its best reading; ties keep the earlier reading.
func rankSourceEntry(entry SourceIndexEntry, readings []sourcePathReading) (rankedSourceFile, bool) {
	var best rankedSourceFile
	found := false
	for index, reading := range readings {
		if reading.anchored() && reading.rootID != entry.RootID {
			continue
		}
		item, ok := rankSourceReading(entry, reading)
		if !ok {
			continue
		}
		item.reading = index
		if !found || compareSourceFile(item, best) < 0 {
			best, found = item, true
		}
	}
	return best, found
}

func rankSourceReading(entry SourceIndexEntry, reading sourcePathReading) (rankedSourceFile, bool) {
	item := rankedSourceFile{entry: entry}
	if reading.anchored() {
		// The window starts inside the root's path, so it ends where rest first completes.
		indexes, ok := greedySubsequence([]rune(strings.ToLower(entry.Path)), []rune(reading.rest), 0)
		item.tier, item.score = 2, reading.anchor
		if len(indexes) > 0 {
			item.score += indexes[len(indexes)-1] + 1
		}
		return item, ok
	}
	query := reading.rest
	base := strings.ToLower(filepath.Base(entry.Path))
	switch {
	case strings.HasPrefix(base, query):
		return item, true
	case strings.Contains(base, query):
		item.tier, item.score = 1, strings.Index(base, query)
		return item, true
	default:
		span, ok := sourceIndexSubsequenceSpan(strings.ToLower(entry.Path), query)
		item.tier, item.score = 2, span
		return item, ok
	}
}

func compareSourceFile(a, b rankedSourceFile) int {
	if a.tier != b.tier {
		return a.tier - b.tier
	}
	if a.score != b.score {
		return a.score - b.score
	}
	if len(a.entry.Path) != len(b.entry.Path) {
		return len(a.entry.Path) - len(b.entry.Path)
	}
	if order := strings.Compare(a.entry.RootID, b.entry.RootID); order != 0 {
		return order
	}
	return strings.Compare(a.entry.Path, b.entry.Path)
}

func sourceIndexSubsequenceSpan(text, query string) (int, bool) {
	needles := []rune(query)
	if len(needles) == 0 {
		return 0, true
	}
	starts := make([]int, len(needles))
	for i := range starts {
		starts[i] = -1
	}
	best := 0
	for pos, char := range []rune(text) {
		for i := len(needles) - 1; i >= 0; i-- {
			if char != needles[i] {
				continue
			}
			if i == 0 {
				starts[i] = pos
			} else if starts[i-1] >= 0 {
				starts[i] = starts[i-1]
			}
			if i == len(needles)-1 && starts[i] >= 0 {
				span := pos - starts[i] + 1
				if best == 0 || span < best {
					best = span
				}
			}
		}
	}
	return best, best > 0
}

// sourceHighlights marks what item's reading matched. Code points of a lowercased path line up
// with the original because Go folds case one rune at a time.
func sourceHighlights(item rankedSourceFile, reading sourcePathReading) []SourceTextRange {
	if reading.rest == "" {
		return nil
	}
	path := []rune(strings.ToLower(item.entry.Path))
	needles := []rune(reading.rest)
	if !reading.anchored() && item.tier < 2 {
		lower := strings.ToLower(item.entry.Path)
		base := strings.ToLower(filepath.Base(item.entry.Path))
		start := utf8.RuneCountInString(lower[:len(lower)-len(base)]) + utf8.RuneCountInString(base[:item.score])
		return []SourceTextRange{{Start: start, End: start + len(needles)}}
	}
	var out []SourceTextRange
	for _, index := range readableAlignment([]rune(item.entry.Path), path, needles) {
		if n := len(out); n > 0 && out[n-1].End == index {
			out[n-1].End++
		} else {
			out = append(out, SourceTextRange{Start: index, End: index + 1})
		}
	}
	return out
}

// readableAlignment places needles in folded text in the fewest contiguous runs, preferring runs
// that start a word, so a highlight reads as words rather than scattered letters.
func readableAlignment(original, folded, needles []rune) []int {
	const unmatched = int(^uint(0) >> 1)
	runCost := func(j int) int {
		if j == 0 || strings.ContainsRune("/._- ", original[j-1]) || (unicode.IsUpper(original[j]) && unicode.IsLower(original[j-1])) {
			return 3
		}
		return 4
	}
	cost := make([][]int, len(needles))
	from := make([][]int, len(needles))
	for i := range needles {
		cost[i] = make([]int, len(folded))
		from[i] = make([]int, len(folded))
		// Cheapest placement of the previous needle that leaves a gap before j.
		gapCost, gapAt := unmatched, -1
		for j := range folded {
			cost[i][j] = unmatched
			if i > 0 && j >= 2 && cost[i-1][j-2] <= gapCost {
				gapCost, gapAt = cost[i-1][j-2], j-2
			}
			if folded[j] != needles[i] {
				continue
			}
			if i == 0 {
				cost[i][j], from[i][j] = runCost(j), -1
				continue
			}
			if gapCost != unmatched {
				cost[i][j], from[i][j] = gapCost+runCost(j), gapAt
			}
			if j >= 1 && cost[i-1][j-1] != unmatched && cost[i-1][j-1] <= cost[i][j] {
				cost[i][j], from[i][j] = cost[i-1][j-1], j-1
			}
		}
	}
	last := len(needles) - 1
	end := -1
	for j := range folded {
		if cost[last][j] != unmatched && (end < 0 || cost[last][j] < cost[last][end]) {
			end = j
		}
	}
	if end < 0 {
		return nil
	}
	indexes := make([]int, len(needles))
	for i := last; i >= 0; i-- {
		indexes[i] = end
		end = from[i][end]
	}
	return indexes
}

// greedySubsequence matches needles at their earliest positions from start.
func greedySubsequence(text, needles []rune, start int) ([]int, bool) {
	indexes := make([]int, 0, len(needles))
	for pos := start; pos < len(text) && len(indexes) < len(needles); pos++ {
		if text[pos] == needles[len(indexes)] {
			indexes = append(indexes, pos)
		}
	}
	return indexes, len(indexes) == len(needles)
}
