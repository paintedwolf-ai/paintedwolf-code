package portablepython

import (
	"bytes"
	"crypto/sha256"
	"encoding/csv"
	"encoding/hex"
	"fmt"
	"math/big"
	"net/url"
	"path"
	"regexp"
	"sort"
	"strings"
)

// LinkAuditRow is one local markdown reference record.
type LinkAuditRow struct {
	Source string
	Target string
	Exists bool
}

var mdLinkRe = regexp.MustCompile(`\[[^\]]*\]\(([^)]+)\)`)

// ComputeMarkdownLinks independently audits relative markdown links across files.
func ComputeMarkdownLinks(files map[string]string) []LinkAuditRow {
	names := make([]string, 0, len(files))
	for k := range files {
		if strings.HasSuffix(k, ".md") {
			names = append(names, k)
		}
	}
	sort.Strings(names)
	var rows []LinkAuditRow
	for _, src := range names {
		for _, m := range mdLinkRe.FindAllStringSubmatch(files[src], -1) {
			raw := strings.TrimSpace(m[1])
			if strings.HasPrefix(raw, "<") && strings.HasSuffix(raw, ">") {
				raw = raw[1 : len(raw)-1]
			}
			if raw == "" || strings.HasPrefix(raw, "http://") || strings.HasPrefix(raw, "https://") ||
				strings.HasPrefix(raw, "mailto:") || strings.HasPrefix(raw, "#") {
				continue
			}
			target := strings.SplitN(raw, "#", 2)[0]
			if target == "" {
				continue
			}
			decoded, err := url.PathUnescape(target)
			if err != nil {
				decoded = target
			}
			resolved := path.Clean(path.Join(path.Dir(src), decoded))
			_, exists := files[resolved]
			rows = append(rows, LinkAuditRow{Source: src, Target: target, Exists: exists})
		}
	}
	return rows
}

// GradeMarkdownLinks compares candidate links.csv against independently computed link audit rows.
func GradeMarkdownLinks(files map[string]string, csvBytes []byte) error {
	want := ComputeMarkdownLinks(files)
	records, err := csv.NewReader(bytes.NewReader(csvBytes)).ReadAll()
	if err != nil {
		return fmt.Errorf("parse links CSV: %w", err)
	}
	if len(records) < 1 {
		return fmt.Errorf("links CSV is empty")
	}
	got := make([]LinkAuditRow, 0, len(records)-1)
	for _, rec := range records[1:] {
		if len(rec) != 3 {
			return fmt.Errorf("expected 3 columns, got %d", len(rec))
		}
		exists := strings.EqualFold(rec[2], "true")
		got = append(got, LinkAuditRow{Source: rec[0], Target: rec[1], Exists: exists})
	}
	if len(got) != len(want) {
		return fmt.Errorf("link count mismatch: want %d (%+v), got %d (%+v)", len(want), want, len(got), got)
	}
	for i := range want {
		if got[i] != want[i] {
			return fmt.Errorf("link row %d mismatch: want %+v, got %+v", i, want[i], got[i])
		}
	}
	return nil
}

// StreamedProvenanceObs captures candidate digest and instrumented read chunk telemetry.
type StreamedProvenanceObs struct {
	CandidateSHA256 string
	ReadChunkSizes  []int
	MaxChunkSize    int
}

// ComputeStreamedProvenanceHash hashes sorted relpath + NUL + contents independently.
func ComputeStreamedProvenanceHash(files map[string][]byte) string {
	names := make([]string, 0, len(files))
	for k := range files {
		names = append(names, k)
	}
	sort.Strings(names)
	h := sha256.New()
	for _, name := range names {
		_, _ = h.Write([]byte(name))
		_, _ = h.Write([]byte{0})
		_, _ = h.Write(files[name])
	}
	return hex.EncodeToString(h.Sum(nil))
}

// GradeStreamedProvenance verifies digest equality, multi-chunk input shape, and bounded chunk reads.
func GradeStreamedProvenance(files map[string][]byte, obs StreamedProvenanceObs) error {
	if len(files) < 2 {
		return fmt.Errorf("streamed provenance fixture requires at least 2 paths, got %d", len(files))
	}
	if obs.MaxChunkSize <= 0 {
		return fmt.Errorf("max chunk size must be positive")
	}
	hasMultiChunkTail := false
	for _, data := range files {
		if len(data) > 2*obs.MaxChunkSize && len(data)%obs.MaxChunkSize != 0 {
			hasMultiChunkTail = true
			break
		}
	}
	if !hasMultiChunkTail {
		return fmt.Errorf("fixture lacks a file longer than 2 chunks (%d) with a nonempty tail", 2*obs.MaxChunkSize)
	}
	if len(obs.ReadChunkSizes) == 0 {
		return fmt.Errorf("missing instrumented stream read chunk sizes")
	}
	for i, sz := range obs.ReadChunkSizes {
		if sz <= 0 || sz > obs.MaxChunkSize {
			return fmt.Errorf("unbounded or invalid stream read at call %d: size=%d max=%d", i, sz, obs.MaxChunkSize)
		}
	}
	want := ComputeStreamedProvenanceHash(files)
	if obs.CandidateSHA256 != want {
		return fmt.Errorf("provenance SHA-256 mismatch: want %s, got %s", want, obs.CandidateSHA256)
	}
	return nil
}

// PassK computes exact rational C(c, k) / C(n, k) matching pass_k(c, n, k).
func PassK(c, n, k int64) (*big.Rat, error) {
	if n < 0 || c < 0 || k < 0 || c > n || k > n {
		return nil, fmt.Errorf("invalid pass_k arguments: c=%d n=%d k=%d", c, n, k)
	}
	if c < k {
		return big.NewRat(0, 1), nil
	}
	num := new(big.Int).Binomial(c, k)
	den := new(big.Int).Binomial(n, k)
	return new(big.Rat).SetFrac(num, den), nil
}

// UnittestRunObs records one unittest discovery execution outcome.
type UnittestRunObs struct {
	ExitCode int
	Stderr   string
	Passed   bool
}

// ProjectMathObs holds the required pass_k rational cases, invalid-input check, and unittest outcomes.
type ProjectMathObs struct {
	Ratios               map[[3]int64]string
	InvalidInputRejected bool
	FailRun              UnittestRunObs
	PassRun              UnittestRunObs
}

// GradeProjectMathAndTests requires exact 0, 1, and pass_k(3,5,2)==3/10, invalid-input rejection,
// and failing-then-repaired unittest discovery runs.
func GradeProjectMathAndTests(obs ProjectMathObs) error {
	if len(obs.Ratios) == 0 {
		return fmt.Errorf("pass_k ratios map is empty")
	}
	if !obs.InvalidInputRejected {
		return fmt.Errorf("fixture-declared invalid-input behavior was not observed/rejected")
	}
	var sawZero, sawOne, sawThreeTenths bool
	for args, gotStr := range obs.Ratios {
		want, err := PassK(args[0], args[1], args[2])
		if err != nil {
			return err
		}
		got, ok := new(big.Rat).SetString(gotStr)
		if !ok {
			return fmt.Errorf("unparseable rational %q for %v", gotStr, args)
		}
		if got.Cmp(want) != 0 {
			return fmt.Errorf("pass_k%v mismatch: want %s, got %s", args, want.RatString(), got.RatString())
		}
		if want.Sign() == 0 {
			sawZero = true
		}
		if want.Cmp(big.NewRat(1, 1)) == 0 {
			sawOne = true
		}
		if args == [3]int64{3, 5, 2} && want.Cmp(big.NewRat(3, 10)) == 0 {
			sawThreeTenths = true
		}
	}
	if !sawZero {
		return fmt.Errorf("missing required exact-zero pass_k case")
	}
	if !sawOne {
		return fmt.Errorf("missing required exact-one pass_k case")
	}
	if !sawThreeTenths {
		return fmt.Errorf("missing required pass_k(3,5,2) == 3/10 case")
	}
	if obs.FailRun.ExitCode == 0 || !strings.Contains(obs.FailRun.Stderr, "Traceback") {
		return fmt.Errorf("failing unittest must exit nonzero with traceback: exit=%d", obs.FailRun.ExitCode)
	}
	if obs.PassRun.ExitCode != 0 || !obs.PassRun.Passed {
		return fmt.Errorf("repaired unittest must pass with exit 0: exit=%d passed=%v", obs.PassRun.ExitCode, obs.PassRun.Passed)
	}
	return nil
}

