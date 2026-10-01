package survey

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/lycaon/lycaon/internal/evidence"
	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/runeclamp"
)

// SnapshotStats reports merged worker envelope size and snapshot record count.
type SnapshotStats struct {
	MergedBytes int
	WorkerCount int
	RecordCount int
}

// SnapshotInput builds a worker-union candidate ledger for synthesis curate.
type SnapshotInput struct {
	Envelopes   []WorkerEnvelope
	MergedBytes int
	WorkerCount int
	Reader      guidance.EvidenceLedgerReader
}

// ShouldCurateSynthesis implements the byte thresholds in
// docs/tools.md § Synthesis evidence curate.
func ShouldCurateSynthesis(stats SnapshotStats) bool {
	if stats.WorkerCount == 0 || stats.MergedBytes == 0 {
		return false
	}
	if stats.MergedBytes > SynthesisEvidenceBudget {
		return true
	}
	return stats.WorkerCount >= 2 && stats.MergedBytes > SynthesisEvidenceMin
}

// BuildSynthesisSnapshot merges worker report excerpts and child survey evidence.
func BuildSynthesisSnapshot(ctx context.Context, in SnapshotInput) (evidence.Ledger, SnapshotStats, error) {
	stats := SnapshotStats{
		MergedBytes: in.MergedBytes,
		WorkerCount: in.WorkerCount,
	}
	var records []evidence.Record
	seenPathLine := map[string]struct{}{}
	seenChild := map[string]struct{}{}

	for i, env := range in.Envelopes {
		records = appendWorkerReportRecords(records, env, i)
		childID := strings.TrimSpace(env.ChildSessionID)
		if childID == "" || in.Reader == nil {
			continue
		}
		if _, dup := seenChild[childID]; dup {
			continue
		}
		seenChild[childID] = struct{}{}
		childEv, err := in.Reader.LoadLedger(ctx, childID)
		if err != nil {
			return evidence.InitLedger(), SnapshotStats{}, err
		}
		legID := strings.TrimSpace(env.JobID)
		if legID == "" {
			legID = childID
		}
		childEv = evidence.NamespaceLedger(childEv, legID)
		for _, handle := range sortedLedgerHandles(childEv) {
			rec, ok := childEv.Handles[handle]
			if !ok || !isSurveyEvidenceRecord(rec) {
				continue
			}
			key := surveyRecordDedupeKey(rec)
			if _, dup := seenPathLine[key]; dup && key != "" {
				continue
			}
			if key != "" {
				seenPathLine[key] = struct{}{}
			}
			records = append(records, rec)
		}
	}

	ledger := evidence.AssembleLedger(records)
	stats.RecordCount = len(records)
	return ledger, stats, nil
}

func isSurveyEvidenceRecord(rec evidence.Record) bool {
	if rec.IsGate() || !rec.Survey {
		return false
	}
	tool := strings.ToLower(strings.TrimSpace(rec.SourceTool))
	kind := strings.ToLower(strings.TrimSpace(rec.Kind))
	if tool == "command" || kind == "command" {
		return false
	}
	switch kind {
	case "grep", "read", "find", "list", "survey", "worker_report", "finding":
		return true
	default:
		return evidence.ActiveBinding().IsSurveyKind(kind)
	}
}

func surveyRecordDedupeKey(rec evidence.Record) string {
	path := strings.TrimSpace(rec.Path)
	if path == "" {
		return ""
	}
	line := 0
	if len(rec.LineRanges) > 0 {
		line = rec.LineRanges[0].Start
	}
	return path + "|" + fmt.Sprintf("%d", line)
}

func appendWorkerReportRecords(records []evidence.Record, env WorkerEnvelope, idx int) []evidence.Record {
	prefix := workerRecordPrefix(env, idx)
	report := env.Report

	if body := strings.TrimSpace(env.Body); body != "" {
		records = append(records, workerTextRecord(prefix+"-body", "body", runeclamp.Clamp(body, SynthesisWorkerReportExcerpt)))
	}
	if brief := strings.TrimSpace(report.Brief); brief != "" {
		records = append(records, workerTextRecord(prefix+"-brief", "brief", runeclamp.Clamp(brief, SynthesisWorkerReportExcerpt)))
	}
	for j, obj := range report.ObjectivesMet {
		if line := strings.TrimSpace(obj); line != "" {
			records = append(records, workerTextRecord(fmt.Sprintf("%s-objective-%d", prefix, j+1), "objective", line))
		}
	}
	for j, risk := range report.RemainingRisk {
		if line := strings.TrimSpace(risk); line != "" {
			records = append(records, workerTextRecord(fmt.Sprintf("%s-risk-%d", prefix, j+1), "remaining_risk", line))
		}
	}
	for j, path := range report.FilesModified {
		path = strings.TrimSpace(path)
		if path == "" {
			continue
		}
		records = append(records, evidence.Record{
			Handle:     fmt.Sprintf("%s-path-%d", prefix, j+1),
			Kind:       "worker_report",
			Shape:      evidence.ShapeFileRegion,
			SourceTool: "worker_completion",
			Survey:     true,
			Path:       path,
		})
	}
	for j, f := range report.Findings {
		path := strings.TrimSpace(f.Path)
		excerpt := strings.TrimSpace(f.Excerpt)
		if path == "" && excerpt == "" {
			continue
		}
		line := f.Line
		if line <= 0 {
			line = 1
		}
		body := excerpt
		if body == "" {
			body = strings.TrimSpace(f.Note)
		}
		if claim := strings.TrimSpace(f.Claim); claim != "" {
			if body == "" {
				body = claim
			} else {
				body = claim + ": " + body
			}
		}
		records = append(records, evidence.Record{
			Handle:     fmt.Sprintf("%s-finding-%d", prefix, j+1),
			Kind:       "finding",
			Shape:      evidence.ShapeFileRegion,
			SourceTool: "record_finding",
			Survey:     true,
			Path:       path,
			LineRanges: []evidence.LineRange{{Start: line, End: line}},
			Body:       []string{runeclamp.Clamp(body, 240)},
		})
	}
	return records
}

func workerRecordPrefix(env WorkerEnvelope, idx int) string {
	if job := strings.TrimSpace(env.JobID); job != "" {
		return "worker-" + job
	}
	return fmt.Sprintf("worker-%d", idx+1)
}

func workerTextRecord(handle, field, text string) evidence.Record {
	return evidence.Record{
		Handle:     handle,
		Kind:       "worker_report",
		Shape:      evidence.ShapeFileRegion,
		SourceTool: "worker_completion",
		Survey:     true,
		Body:       []string{field + ": " + strings.TrimSpace(text)},
	}
}

func sortedLedgerHandles(ev evidence.Ledger) []string {
	if len(ev.Handles) == 0 {
		return nil
	}
	out := make([]string, 0, len(ev.Handles))
	for handle := range ev.Handles {
		out = append(out, handle)
	}
	sort.Strings(out)
	return out
}
