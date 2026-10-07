package evidence

import (
	"sort"
	"strings"

	"github.com/lycaon/lycaon/internal/sandbox"
)

// ObservedCitation is one ledger token used for citation membership.
type ObservedCitation struct {
	Token    string
	Path     string
	Line     int
	URL      string
	Kind     string
	Fidelity string
}

// InitLedger returns an empty evidence ledger.
func InitLedger() Ledger {
	return Ledger{
		Handles:      make(map[string]Record),
		ByPath:       make(map[string][]string),
		PathFidelity: make(map[string]string),
	}
}

func indexLedgerPath(ev *Ledger, handle, relPath, tier string) {
	if ev == nil || handle == "" {
		return
	}
	relPath = NormalizeLedgerPath(relPath)
	if relPath == "" {
		return
	}
	ev.ByPath[relPath] = append(ev.ByPath[relPath], handle)
	if ev.PathFidelity == nil {
		ev.PathFidelity = make(map[string]string)
	}
	ev.PathFidelity[relPath] = mergePathFidelity(ev.PathFidelity[relPath], tier)
}

func ledgerPaths(ev Ledger) map[string]struct{} {
	out := make(map[string]struct{})
	for path := range ev.ByPath {
		if path != "" {
			out[path] = struct{}{}
		}
	}
	return out
}

// PathObserved reports whether path is in ByPath.
func PathObserved(ev Ledger, relPath string) bool {
	relPath = NormalizeLedgerPath(relPath)
	if relPath == "" {
		return false
	}
	_, ok := ledgerPaths(ev)[relPath]
	return ok
}

// ObservedURLsFromEvidence collects every URL recorded on ledger handles — the primary
// plus every URL the result body named, so a citation of any search result grounds.
func ObservedURLsFromEvidence(ev Ledger) map[string]struct{} {
	out := make(map[string]struct{})
	for _, rec := range ev.Handles {
		for _, u := range ObservedURLsForRecord(rec) {
			out[u] = struct{}{}
		}
	}
	return out
}

// URLSeen reports whether url appeared in web tool evidence for this run.
func (ev Ledger) URLSeen(url string) bool {
	return VerifyURLObserved(ev, url)
}

// ObservedPathsSorted returns normalized repo-relative paths from evidence in stable order.
func ObservedPathsSorted(ev Ledger) []string {
	paths := ledgerPaths(ev)
	if len(paths) == 0 {
		return nil
	}
	out := make([]string, 0, len(paths))
	for p := range paths {
		out = append(out, p)
	}
	sort.Strings(out)
	return out
}

// ObservedURLsSorted returns observed URLs in stable order.
func ObservedURLsSorted(ev Ledger) []string {
	urls := ObservedURLsFromEvidence(ev)
	if len(urls) == 0 {
		return nil
	}
	out := make([]string, 0, len(urls))
	for u := range urls {
		out = append(out, u)
	}
	sort.Strings(out)
	return out
}

func (rec *Record) touchPath(projectDir, token, tier string) {
	if rec == nil {
		return
	}
	if tier == "" {
		tier = FidelityStructured
	}
	rel := normalizedEvidencePath(projectDir, token)
	if rel == "" {
		return
	}
	if rec.Path == "" {
		rec.Path = rel
	}
	if rec.Fidelity == "" {
		rec.Fidelity = tier
	} else {
		rec.Fidelity = mergePathFidelity(rec.Fidelity, tier)
	}
	if rec.pathTiers == nil {
		rec.pathTiers = make(map[string]string)
	}
	rec.pathTiers[rel] = mergePathFidelity(rec.pathTiers[rel], tier)
	for _, existing := range rec.pathsTouched {
		if existing == rel {
			return
		}
	}
	rec.pathsTouched = append(rec.pathsTouched, rel)
}

// touchURL records a URL the tool result named: the first becomes the primary (the
// persisted url column), and every distinct URL is retained so a citation of any one
// of them grounds — the URL parallel to touchPath.
func (rec *Record) touchURL(rawURL string) {
	if rec == nil {
		return
	}
	url := strings.TrimSpace(rawURL)
	if url == "" {
		return
	}
	if rec.URL == "" {
		rec.URL = url
	}
	for _, existing := range rec.urlsTouched {
		if existing == url {
			return
		}
	}
	rec.urlsTouched = append(rec.urlsTouched, url)
}

// ObservedURLsForRecord returns distinct URLs observed on a record.
func ObservedURLsForRecord(rec Record) []string {
	seen := map[string]struct{}{}
	var out []string
	add := func(u string) {
		u = strings.TrimSpace(u)
		if u == "" {
			return
		}
		if _, ok := seen[u]; ok {
			return
		}
		seen[u] = struct{}{}
		out = append(out, u)
	}
	add(rec.URL)
	for _, u := range rec.urlsTouched {
		add(u)
	}
	return out
}

// touchURLTitle remembers the title a tool reported for a page it observed.
func (rec *Record) touchURLTitle(rawURL, title string) {
	if rec == nil {
		return
	}
	rawURL, title = strings.TrimSpace(rawURL), strings.TrimSpace(title)
	if rawURL == "" || title == "" {
		return
	}
	if rec.urlTitles == nil {
		rec.urlTitles = make(map[string]string)
	}
	rec.urlTitles[rawURL] = title
}

// URLTitles returns the page titles this record observed, keyed by URL.
func (rec Record) URLTitles() map[string]string {
	if len(rec.urlTitles) == 0 {
		return nil
	}
	out := make(map[string]string, len(rec.urlTitles))
	for u, t := range rec.urlTitles {
		out[u] = t
	}
	return out
}

// GrepMatchStats counts a search record's matching lines and the distinct
// files they fell in. Zero for records that hold no match lines.
func GrepMatchStats(rec Record) (lines, paths int) {
	for _, byLine := range rec.grepLines {
		if len(byLine) == 0 {
			continue
		}
		paths++
		lines += len(byLine)
	}
	return lines, paths
}

func pathTrustOnRecord(rec Record, path string) string {
	path = NormalizeLedgerPath(path)
	if rec.pathTiers != nil {
		if t, ok := rec.pathTiers[path]; ok && t != "" {
			return t
		}
	}
	if t := strings.TrimSpace(rec.Fidelity); t != "" {
		return t
	}
	return FidelityStructured
}

func normalizedEvidencePath(projectDir, token string) string {
	if rel, ok := NormalizeCitationPath(projectDir, token); ok {
		return rel
	}
	if strings.TrimSpace(projectDir) != "" {
		return ""
	}
	rel := NormalizeLedgerPath(token)
	if rel == "" || sandbox.HasParentTraversal(rel) {
		return ""
	}
	return rel
}

func IndexedPathsForRecord(rec Record) []string {
	seen := map[string]struct{}{}
	var out []string
	add := func(p string) {
		p = NormalizeLedgerPath(p)
		if p == "" {
			return
		}
		if _, ok := seen[p]; ok {
			return
		}
		seen[p] = struct{}{}
		out = append(out, p)
	}
	add(rec.Path)
	for _, p := range rec.pathsTouched {
		add(p)
	}
	for p := range rec.grepLines {
		add(p)
	}
	return out
}

func (rec *Record) addGrepLine(relPath string, line int, content string) {
	if rec == nil || relPath == "" || line <= 0 {
		return
	}
	if rec.grepLines == nil {
		rec.grepLines = make(map[string]map[int]string)
	}
	if rec.grepLines[relPath] == nil {
		rec.grepLines[relPath] = make(map[int]string)
	}
	if strings.TrimSpace(content) != "" {
		rec.grepLines[relPath][line] = content
	}
}

// LineRangesCover reports whether the line ranges in newRanges completely cover
// all line ranges in oldRanges. An oldRanges slice with zero ranges counts as covered.
func LineRangesCover(newRanges, oldRanges []LineRange) bool {
	if len(oldRanges) == 0 {
		return true
	}
	if len(newRanges) == 0 {
		return false
	}
	mergedNew := mergeLineRanges(newRanges)
	mergedOld := mergeLineRanges(oldRanges)
	for _, o := range mergedOld {
		covered := false
		for _, n := range mergedNew {
			if n.Start <= o.Start && n.End >= o.End {
				covered = true
				break
			}
		}
		if !covered {
			return false
		}
	}
	return true
}

func mergeLineRanges(ranges []LineRange) []LineRange {
	if len(ranges) <= 1 {
		return append([]LineRange(nil), ranges...)
	}
	sorted := make([]LineRange, 0, len(ranges))
	for _, r := range ranges {
		if r.Start > 0 && r.End >= r.Start {
			sorted = append(sorted, r)
		}
	}
	if len(sorted) <= 1 {
		return sorted
	}
	sort.Slice(sorted, func(i, j int) bool {
		if sorted[i].Start == sorted[j].Start {
			return sorted[i].End < sorted[j].End
		}
		return sorted[i].Start < sorted[j].Start
	})
	out := []LineRange{sorted[0]}
	for _, next := range sorted[1:] {
		last := &out[len(out)-1]
		if next.Start <= last.End+1 {
			if next.End > last.End {
				last.End = next.End
			}
		} else {
			out = append(out, next)
		}
	}
	return out
}

