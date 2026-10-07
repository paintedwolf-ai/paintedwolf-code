package workflowadmin

import (
	"maps"
	"slices"

	"github.com/lycaon/lycaon/internal/report"
	"github.com/lycaon/lycaon/internal/scan"
	wire "github.com/lycaon/lycaon/pkg/api"
)

// scanCoverage summarizes execution independently of finding inventory accounting.
type scanCoverage struct {
	coverage []report.ReportCoverageItem
	gaps     []report.ReportGap
	scanners int
	failed   int
}

func summarizeScanCoverage(scans []wire.CodeScan) scanCoverage {
	var out scanCoverage
	scanners := map[string]bool{}
	failed := map[string]bool{}
	for _, s := range scans {
		scanners[s.ScannerID] = true
		out.coverage = append(out.coverage, report.ReportCoverageItem{
			Subject: s.ScannerID, Status: string(s.Status), Detail: scanCoverageDetail(s),
		})
	}
	for _, s := range scan.UnrecoveredScans(scans) {
		failed[s.ScannerID] = true
	}
	out.gaps = append(out.gaps, report.ReportGap{Kind: report.GapScansFailed, Count: len(failed), Of: len(scanners), Names: slices.Sorted(maps.Keys(failed))})

	moved := report.ReportGap{Kind: report.GapScansMoved, Of: len(scanners)}
	standing := report.ReportGap{Kind: report.GapScansStanding, Of: len(scanners)}
	movedFiles, movedScanners := map[string]bool{}, map[string]bool{}
	standingFiles, standingScanners := map[string]bool{}, map[string]bool{}
	for _, s := range scans {
		for _, w := range s.Warnings {
			if !scan.WarningRetryable(w.Kind) && w.File != "" {
				standingFiles[w.File] = true
			}
		}
	}
	for _, g := range scan.RunCoverageGaps(scans) {
		if g.Open() {
			movedScanners[g.Scanner] = true
			for _, path := range g.Moved {
				if path == "" {
					moved.UnknownScope = true
				} else {
					movedFiles[path] = true
				}
			}
		}
		if g.Standing > 0 {
			standingScanners[g.Scanner] = true
			standing.Detail += g.Standing
		}
	}
	moved.Count = len(movedScanners)
	moved.Names = slices.Sorted(maps.Keys(movedScanners))
	moved.Detail = len(movedFiles)
	out.gaps = append(out.gaps, moved)
	standing.Count = len(standingScanners)
	standing.Names = slices.Sorted(maps.Keys(standingScanners))
	standing.DetailFiles = len(standingFiles)
	out.gaps = append(out.gaps, standing)

	out.scanners, out.failed = len(scanners), len(failed)
	return out
}
