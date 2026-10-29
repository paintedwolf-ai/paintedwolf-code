//go:build scanstress

package scanstress

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	scanoutput "github.com/lycaon/lycaon/internal/scan/output"
	"github.com/lycaon/lycaon/internal/testutil"
)

const resourceRule = "opengrep:lycaon.swift.secure-coding-disabled"

// One declared finding per function keeps fixture size independent of rule breadth.
func writeResourceCorpus(t *testing.T, files, matches int) string {
	t.Helper()
	dir := t.TempDir()
	for file := range files {
		var source strings.Builder
		source.WriteString("import Foundation\n")
		for match := range matches {
			fmt.Fprintf(&source, "func load_%d_%d(decoder: NSKeyedUnarchiver) { decoder.requiresSecureCoding = false }\n", file, match)
		}
		testutil.FailErr(t, "write resource corpus", os.WriteFile(filepath.Join(dir, fmt.Sprintf("part_%03d.swift", file)), []byte(source.String()), 0o600))
	}
	return dir
}

func requireResourceFindings(t *testing.T, result *scanoutput.Result, files, matches int) {
	t.Helper()
	if result == nil {
		t.Fatal("resource scan returned no result")
	}
	if result.FindingsCount != files*matches || len(result.Findings) != files*matches {
		t.Fatalf("resource scan returned %d findings and %d entries, want %d", result.FindingsCount, len(result.Findings), files*matches)
	}
	if len(result.ScannedPaths) != files || len(result.Warnings) != 0 {
		t.Fatalf("resource scan covered %d of %d paths with warnings: %+v", len(result.ScannedPaths), files, result.Warnings)
	}
	for _, finding := range result.Findings {
		if finding.RuleID != resourceRule {
			t.Fatalf("resource fixture returned rule %s, want %s", finding.RuleID, resourceRule)
		}
	}
}

func requireResourceBounds(t *testing.T, samples []treeSample, scans int) {
	t.Helper()
	// These ceilings cover this fixture's host and each simultaneous engine tree.
	const hostKiB = 256 << 10
	const engineKiB = 768 << 10
	var peakHost, peakTree, peakRuns int
	for _, sample := range samples {
		peakHost = max(peakHost, sample.SelfRSS)
		peakTree = max(peakTree, sample.AggregateRSS)
		peakRuns = max(peakRuns, sample.Runs)
	}
	if peakHost == 0 || peakRuns < scans {
		t.Fatalf("resource observation missed active scans: host=%d KiB simultaneous_runs=%d want=%d", peakHost, peakRuns, scans)
	}
	if peakHost > hostKiB || peakTree > hostKiB+scans*engineKiB {
		t.Errorf("scanner RSS exceeded fixture budget: host=%d/%d KiB tree=%d/%d KiB", peakHost, hostKiB, peakTree, hostKiB+scans*engineKiB)
	}
}
