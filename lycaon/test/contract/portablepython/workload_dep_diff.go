package portablepython

import (
	"bytes"
	"encoding/csv"
	"fmt"
	"regexp"
	"sort"
	"strings"

	"github.com/BurntSushi/toml"
)

// DepChangeRow represents one version change across dependency inventories.
type DepChangeRow struct {
	Source     string
	Package    string
	OldVersion string
	NewVersion string
}

// DepDiffExpectation holds independent expectations for Lockfile & requirements diff.
type DepDiffExpectation struct {
	Rows          []DepChangeRow
	Discrepancies map[string][]string // "before"/"after" -> sorted normalized discrepant names
}

var reqPinRe = regexp.MustCompile(`^([A-Za-z0-9_.-]+)(?:\[[A-Za-z0-9_,.-]*\])?==(.+)$`)

func parseUVLock(data []byte) (map[string]string, error) {
	var parsed struct {
		Package []struct {
			Name    string `toml:"name"`
			Version string `toml:"version"`
		} `toml:"package"`
	}
	if _, err := toml.Decode(string(data), &parsed); err != nil {
		return nil, err
	}
	out := make(map[string]string, len(parsed.Package))
	for _, p := range parsed.Package {
		if p.Version != "" {
			out[p.Name] = p.Version
		}
	}
	return out, nil
}

func parseRequirements(data []byte) map[string]string {
	out := make(map[string]string)
	for _, line := range strings.Split(string(data), "\n") {
		trim := strings.TrimSpace(line)
		if trim == "" || strings.HasPrefix(trim, "#") {
			continue
		}
		if m := reqPinRe.FindStringSubmatch(trim); len(m) == 3 {
			out[m[1]] = m[2]
		}
	}
	return out
}

func normalizePkg(name string) string {
	return strings.ReplaceAll(strings.ToLower(name), "_", "-")
}

// ComputeDepDiff independently derives expected dependency-change rows and shared-pin discrepancies.
func ComputeDepDiff(beforeLock, afterLock, beforeReq, afterReq []byte) (DepDiffExpectation, error) {
	bLock, err := parseUVLock(beforeLock)
	if err != nil {
		return DepDiffExpectation{}, fmt.Errorf("parse before uv.lock: %w", err)
	}
	aLock, err := parseUVLock(afterLock)
	if err != nil {
		return DepDiffExpectation{}, fmt.Errorf("parse after uv.lock: %w", err)
	}
	bReq := parseRequirements(beforeReq)
	aReq := parseRequirements(afterReq)

	var rows []DepChangeRow
	diffMaps := func(src string, before, after map[string]string) {
		seen := make(map[string]struct{}, len(before)+len(after))
		for k := range before {
			seen[k] = struct{}{}
		}
		for k := range after {
			seen[k] = struct{}{}
		}
		names := make([]string, 0, len(seen))
		for k := range seen {
			names = append(names, k)
		}
		sort.Strings(names)
		for _, pkg := range names {
			if before[pkg] != after[pkg] {
				rows = append(rows, DepChangeRow{
					Source:     src,
					Package:    pkg,
					OldVersion: before[pkg],
					NewVersion: after[pkg],
				})
			}
		}
	}
	diffMaps("uv.lock", bLock, aLock)
	diffMaps("train-host/requirements-cuda.txt", bReq, aReq)

	findDiscrepancies := func(lock, req map[string]string) []string {
		normLock := make(map[string]string, len(lock))
		for k, v := range lock {
			normLock[normalizePkg(k)] = v
		}
		var bad []string
		for k, v := range req {
			nk := normalizePkg(k)
			if lv, ok := normLock[nk]; ok && lv != v {
				bad = append(bad, nk)
			}
		}
		sort.Strings(bad)
		return bad
	}

	return DepDiffExpectation{
		Rows: rows,
		Discrepancies: map[string][]string{
			"before": findDiscrepancies(bLock, bReq),
			"after":  findDiscrepancies(aLock, aReq),
		},
	}, nil
}

// GradeDependencyDiff verifies candidate CSV bytes and reported discrepancies against independent expectations.
func GradeDependencyDiff(exp DepDiffExpectation, csvBytes []byte, reportedDiscrepancies map[string][]string) error {
	records, err := csv.NewReader(bytes.NewReader(csvBytes)).ReadAll()
	if err != nil {
		return fmt.Errorf("parse candidate CSV: %w", err)
	}
	if len(records) < 1 {
		return fmt.Errorf("candidate CSV is empty")
	}
	got := make([]DepChangeRow, 0, len(records)-1)
	for _, rec := range records[1:] {
		if len(rec) != 4 {
			return fmt.Errorf("expected 4 columns, got %d", len(rec))
		}
		got = append(got, DepChangeRow{Source: rec[0], Package: rec[1], OldVersion: rec[2], NewVersion: rec[3]})
	}
	if len(got) != len(exp.Rows) {
		return fmt.Errorf("row count mismatch: want %d, got %d", len(exp.Rows), len(got))
	}
	for i := range exp.Rows {
		if got[i] != exp.Rows[i] {
			return fmt.Errorf("row %d mismatch: want %+v, got %+v", i, exp.Rows[i], got[i])
		}
	}
	for _, tree := range []string{"before", "after"} {
		want := exp.Discrepancies[tree]
		gotD := append([]string(nil), reportedDiscrepancies[tree]...)
		sort.Strings(gotD)
		if strings.Join(want, ",") != strings.Join(gotD, ",") {
			return fmt.Errorf("%s discrepancies mismatch: want %v, got %v", tree, want, gotD)
		}
	}
	return nil
}
