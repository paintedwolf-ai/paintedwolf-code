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

// GradeProjectMathAndTests checks exact rational pass_k outputs plus failing-then-repaired unittest runs.
func GradeProjectMathAndTests(gotRatios map[[3]int64]string, failRun, passRun UnittestRunObs) error {
	for args, gotStr := range gotRatios {
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
	}
	if failRun.ExitCode == 0 || !strings.Contains(failRun.Stderr, "Traceback") {
		return fmt.Errorf("failing unittest must exit nonzero with traceback: exit=%d", failRun.ExitCode)
	}
	if passRun.ExitCode != 0 || !passRun.Passed {
		return fmt.Errorf("repaired unittest must pass with exit 0: exit=%d passed=%v", passRun.ExitCode, passRun.Passed)
	}
	return nil
}

// GradeSplitPreservation verifies original split bytes survive rejected invalid/overlapping inputs
// and repeated valid splits produce byte-identical outputs.
func GradeSplitPreservation(original, afterInvalid, afterOverlap, validRun1, validRun2 map[string][]byte) error {
	for name, origBytes := range original {
		if !bytes.Equal(afterInvalid[name], origBytes) {
			return fmt.Errorf("split file %s mutated after late invalid JSONL rejection", name)
		}
		if !bytes.Equal(afterOverlap[name], origBytes) {
			return fmt.Errorf("split file %s mutated after cross-split overlap rejection", name)
		}
	}
	if len(validRun1) != len(validRun2) {
		return fmt.Errorf("valid split runs produced different file counts: %d vs %d", len(validRun1), len(validRun2))
	}
	for name, b1 := range validRun1 {
		if !bytes.Equal(b1, validRun2[name]) {
			return fmt.Errorf("valid split output %s is not deterministic across runs", name)
		}
	}
	return nil
}

// UsageLedgerEntry represents one usage accounting observation.
type UsageLedgerEntry struct {
	CaseKind     string // "absent_opt_in", "empty_choices", "ordinary"
	RequestID    string
	PromptTokens int
	FinishReason *string
	Interpreter  string // "portable_python" or "project_runner"
}

// GradeUsageLedger verifies usage ledger invariants and interpreter provenance recording.
func GradeUsageLedger(entries []UsageLedgerEntry) error {
	seen := make(map[string]bool)
	for _, e := range entries {
		if e.Interpreter == "" {
			return fmt.Errorf("usage entry %s missing interpreter provenance", e.CaseKind)
		}
		if e.RequestID == "" {
			return fmt.Errorf("usage entry %s missing request ID", e.CaseKind)
		}
		switch e.CaseKind {
		case "empty_choices":
			if e.FinishReason != nil {
				return fmt.Errorf("empty_choices must have null finish_reason, got %q", *e.FinishReason)
			}
		case "ordinary":
			if e.FinishReason == nil || *e.FinishReason == "" {
				return fmt.Errorf("ordinary entry must have non-empty finish_reason")
			}
			if e.PromptTokens <= 0 {
				return fmt.Errorf("ordinary entry must record positive usage tokens")
			}
		case "absent_opt_in":
			// Valid when request ID and interpreter are recorded without fabricating opt-in usage.
		default:
			return fmt.Errorf("unknown usage case kind %q", e.CaseKind)
		}
		seen[e.CaseKind] = true
	}
	for _, req := range []string{"absent_opt_in", "empty_choices", "ordinary"} {
		if !seen[req] {
			return fmt.Errorf("missing required usage ledger case %q", req)
		}
	}
	return nil
}
