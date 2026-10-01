package survey

import (
	"fmt"
	"github.com/lycaon/lycaon/internal/runeclamp"
	"path/filepath"
	"sort"
	"strings"

	"github.com/lycaon/lycaon/internal/evidence"
)

const (
	digestTopPathsMax   = 5
	digestTopExtsMax    = 8
	digestDrillPathsMax = 5
	digestProbeLineMax  = 48
	digestLabelRuneMax  = 128
	digestErrorRuneMax  = 256
)

// ProbeSummary reports one executed probe for digest rendering.
type ProbeSummary struct {
	Label               string
	Kind                ProbeKind
	MatchCount          int // raw probe hits before emit_cap rollup
	SampleCount         int // bounded representative records retained from the full scan
	Sampled             bool
	FilesExamined       int
	FilesBytesTruncated int
	BinarySkipped       int
	FilesUnreadable     int
	Emitted             int // ledger entries contributed
	Folded              int // retained hits outside emitted path groups
	Rolled              bool
	Priority            int
	Err                 string
}

func buildDigest(bundleID, rootPath string, summaries []ProbeSummary, ledger evidence.Ledger, coverage ProbeCoverage) string {
	var b strings.Builder
	total := len(ledger.Handles)
	fmt.Fprintf(&b, "survey bundle=%s path=%s total=%d", bundleID, rootPath, total)
	b.WriteString("\n")
	if coverage.Resolution != "" {
		fmt.Fprintf(&b, "altitude: %s resolution=%s inventory=%s examined=%d groups=%d",
			coverage.Altitude, coverage.Resolution, coverage.InventoryState, coverage.EntriesExamined, coverage.Groups)
		if coverage.InventoryEntries > 0 {
			fmt.Fprintf(&b, " inventory_entries=%d", coverage.InventoryEntries)
		}
		if coverage.GroupsFolded > 0 {
			fmt.Fprintf(&b, " groups_folded=%d", coverage.GroupsFolded)
		}
		b.WriteString("\n")
	}

	if shape := orientationShape(bundleID, summaries, ledger); shape != "" {
		fmt.Fprintf(&b, "shape: %s\n", shape)
	}

	visible, compacted := digestProbeSummaries(summaries)
	sorted := append([]ProbeSummary(nil), visible...)
	sort.Slice(sorted, func(i, j int) bool {
		if sorted[i].Label != sorted[j].Label {
			return sorted[i].Label < sorted[j].Label
		}
		return sorted[i].Kind < sorted[j].Kind
	})
	for _, s := range sorted {
		label := compactDigestText(s.Label, digestLabelRuneMax)
		if s.Err != "" {
			fmt.Fprintf(&b, "- %s (%s): error=%s", label, s.Kind, compactDigestText(s.Err, digestErrorRuneMax))
		} else if s.Rolled {
			fmt.Fprintf(&b, "- %s (%s): %d candidates → %d path groups", label, s.Kind, s.MatchCount, s.Emitted)
			if s.Sampled {
				fmt.Fprintf(&b, " (full candidate pass; representative sample=%d)", s.SampleCount)
			} else if s.Folded > 0 {
				fmt.Fprintf(&b, " (%d hits outside emit_cap paths)", s.Folded)
			}
		} else if s.Sampled {
			fmt.Fprintf(&b, "- %s (%s): %d candidates (full candidate pass; representative sample=%d)",
				label, s.Kind, s.MatchCount, s.SampleCount)
		} else {
			fmt.Fprintf(&b, "- %s (%s): %d candidates", label, s.Kind, s.MatchCount)
		}
		if s.FilesExamined > 0 {
			fmt.Fprintf(&b, " files_examined=%d", s.FilesExamined)
		}
		if s.FilesBytesTruncated > 0 {
			fmt.Fprintf(&b, " bytes_truncated=%d", s.FilesBytesTruncated)
		}
		if s.BinarySkipped > 0 {
			fmt.Fprintf(&b, " binary_skipped=%d", s.BinarySkipped)
		}
		if s.FilesUnreadable > 0 {
			fmt.Fprintf(&b, " unreadable=%d", s.FilesUnreadable)
		}
		b.WriteString("\n")
	}
	if compacted.Probes > 0 {
		fmt.Fprintf(&b, "- compacted probes=%d candidates=%d emitted=%d errors=%d sampled=%d files_examined=%d",
			compacted.Probes, compacted.MatchCount, compacted.Emitted, compacted.Errors,
			compacted.Sampled, compacted.FilesExamined)
		if compacted.FilesBytesTruncated > 0 {
			fmt.Fprintf(&b, " bytes_truncated=%d", compacted.FilesBytesTruncated)
		}
		if compacted.BinarySkipped > 0 {
			fmt.Fprintf(&b, " binary_skipped=%d", compacted.BinarySkipped)
		}
		if compacted.FilesUnreadable > 0 {
			fmt.Fprintf(&b, " unreadable=%d", compacted.FilesUnreadable)
		}
		b.WriteString("\n")
	}

	if drill := coverageDrillHints(coverage, ledger); drill != "" {
		fmt.Fprintf(&b, "drill: %s\n", drill)
	}
	return strings.TrimRight(b.String(), "\n")
}

type compactedProbeSummary struct {
	Probes              int
	MatchCount          int
	Emitted             int
	Errors              int
	Sampled             int
	FilesExamined       int
	FilesBytesTruncated int
	BinarySkipped       int
	FilesUnreadable     int
}

func digestProbeSummaries(summaries []ProbeSummary) ([]ProbeSummary, compactedProbeSummary) {
	if len(summaries) <= digestProbeLineMax {
		return summaries, compactedProbeSummary{}
	}
	ranked := append([]ProbeSummary(nil), summaries...)
	sort.Slice(ranked, func(i, j int) bool {
		if (ranked[i].Err != "") != (ranked[j].Err != "") {
			return ranked[i].Err != ""
		}
		if ranked[i].Priority != ranked[j].Priority {
			return ranked[i].Priority > ranked[j].Priority
		}
		if ranked[i].Label != ranked[j].Label {
			return ranked[i].Label < ranked[j].Label
		}
		return ranked[i].Kind < ranked[j].Kind
	})
	visible := ranked[:digestProbeLineMax]
	var compacted compactedProbeSummary
	for _, summary := range ranked[digestProbeLineMax:] {
		compacted.Probes++
		compacted.MatchCount += summary.MatchCount
		compacted.Emitted += summary.Emitted
		compacted.FilesExamined += summary.FilesExamined
		compacted.FilesBytesTruncated += summary.FilesBytesTruncated
		compacted.BinarySkipped += summary.BinarySkipped
		compacted.FilesUnreadable += summary.FilesUnreadable
		if summary.Err != "" {
			compacted.Errors++
		}
		if summary.Sampled {
			compacted.Sampled++
		}
	}
	return visible, compacted
}

func compactDigestText(value string, maxRunes int) string {
	runes := []rune(value)
	if len(runes) <= maxRunes {
		return value
	}
	return runeclamp.Fit(value, maxRunes)
}

func coverageDrillHints(coverage ProbeCoverage, ledger evidence.Ledger) string {
	if len(coverage.DrillPaths) == 0 {
		return drillHints(ledger)
	}
	paths := coverage.DrillPaths
	if len(paths) > digestDrillPathsMax {
		paths = paths[:digestDrillPathsMax]
	}
	parts := make([]string, 0, len(paths))
	for _, path := range paths {
		parts = append(parts, fmt.Sprintf("summarize(path=%s)", path))
	}
	return strings.Join(parts, " | ")
}

func orientationShape(bundleID string, summaries []ProbeSummary, ledger evidence.Ledger) string {
	rawTotal := 0
	emittedTotal := 0
	rolled := 0
	for _, s := range summaries {
		if s.Err != "" {
			continue
		}
		rawTotal += s.MatchCount
		emittedTotal += s.Emitted
		if s.Rolled {
			rolled++
		}
	}
	parts := make([]string, 0, 4)
	if rawTotal > 0 && rawTotal != emittedTotal {
		parts = append(parts, fmt.Sprintf("raw=%d emitted=%d", rawTotal, emittedTotal))
	}
	if rolled > 0 {
		parts = append(parts, fmt.Sprintf("rolled_probes=%d", rolled))
	}
	if top := topPathCounts(ledger, digestTopPathsMax); top != "" {
		parts = append(parts, "top_paths="+top)
	}
	if bundleID == "layout_overview" {
		if exts := topExtensionCounts(ledger, digestTopExtsMax); exts != "" {
			parts = append(parts, "ext="+exts)
		}
	}
	return strings.Join(parts, " ")
}

func drillHints(ledger evidence.Ledger) string {
	paths := rankedPaths(ledger)
	if len(paths) == 0 {
		return ""
	}
	if len(paths) > digestDrillPathsMax {
		paths = paths[:digestDrillPathsMax]
	}
	parts := make([]string, 0, len(paths))
	for _, p := range paths {
		parts = append(parts, fmt.Sprintf("summarize(path=%s)", p.path))
	}
	return strings.Join(parts, " | ")
}

type pathCount struct {
	path  string
	count int
}

func rankedPaths(ledger evidence.Ledger) []pathCount {
	counts := map[string]int{}
	for _, handle := range evidence.HandlesSorted(ledger) {
		rec := ledger.Handles[handle]
		p := strings.TrimSpace(rec.Path)
		if p == "" || p == "(no-path)" {
			continue
		}
		counts[p]++
	}
	out := make([]pathCount, 0, len(counts))
	for p, n := range counts {
		out = append(out, pathCount{path: p, count: n})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].count != out[j].count {
			return out[i].count > out[j].count
		}
		return out[i].path < out[j].path
	})
	return out
}

func topPathCounts(ledger evidence.Ledger, n int) string {
	paths := rankedPaths(ledger)
	if len(paths) == 0 {
		return ""
	}
	if len(paths) > n {
		paths = paths[:n]
	}
	parts := make([]string, 0, len(paths))
	for _, p := range paths {
		parts = append(parts, fmt.Sprintf("%s:%d", p.path, p.count))
	}
	return strings.Join(parts, ",")
}

func topExtensionCounts(ledger evidence.Ledger, n int) string {
	counts := map[string]int{}
	for _, handle := range evidence.HandlesSorted(ledger) {
		rec := ledger.Handles[handle]
		p := strings.TrimSpace(rec.Path)
		if p == "" {
			continue
		}
		ext := strings.ToLower(strings.TrimPrefix(filepath.Ext(p), "."))
		if ext == "" {
			continue
		}
		counts[ext]++
	}
	type extCount struct {
		ext   string
		count int
	}
	list := make([]extCount, 0, len(counts))
	for e, c := range counts {
		list = append(list, extCount{ext: e, count: c})
	}
	sort.Slice(list, func(i, j int) bool {
		if list[i].count != list[j].count {
			return list[i].count > list[j].count
		}
		return list[i].ext < list[j].ext
	})
	if len(list) == 0 {
		return ""
	}
	if len(list) > n {
		list = list[:n]
	}
	parts := make([]string, 0, len(list))
	for _, e := range list {
		parts = append(parts, fmt.Sprintf("%s:%d", e.ext, e.count))
	}
	return strings.Join(parts, ",")
}
