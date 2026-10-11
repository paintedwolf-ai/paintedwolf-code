package search

import (
	"bytes"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"io"
	"strconv"
	"strings"
)

// ExportRow is one lossless JSONL export record.
type ExportRow struct {
	HitID           string  `json:"hit_id"`
	HitKind         string  `json:"hit_kind"`
	Source          string  `json:"source"`
	Score           float64 `json:"score"`
	SessionID       string  `json:"session_id"`
	ParentSessionID string  `json:"parent_session_id"`
	WorkerID        string  `json:"worker_id"`
	SourceRef       string  `json:"source_ref"`
	ProjectID       string  `json:"project_id"`
	RootID          string  `json:"root_id,omitempty"`
	ProjectName     string  `json:"project_name"`
	Snippet         string  `json:"snippet"`
	TS              string  `json:"ts"`
	LegID           string  `json:"leg_id"`
	Handle          string  `json:"handle"`
	Tool            string  `json:"tool"`
	Title           string  `json:"title"`
	Context         string  `json:"context"`
	Path            string  `json:"path"`
	Line            int     `json:"line"`
	URL             string  `json:"url"`
	Trust           string  `json:"trust"`
	Verified        *bool   `json:"verified"`
	HintCode        string  `json:"hint_code"`
}

type exportMeta struct {
	ExportMeta struct {
		Truncated bool `json:"truncated"`
	} `json:"_export_meta"`
}

// WriteExport streams hits as JSONL or CSV.
func WriteExport(w io.Writer, format string, hits []Hit, truncated bool) error {
	switch strings.ToLower(strings.TrimSpace(format)) {
	case ExportFormatJSONL:
		return writeJSONL(w, hits, truncated)
	case ExportFormatCSV:
		return writeCSV(w, hits, truncated)
	default:
		return fmt.Errorf("unsupported export format %q", format)
	}
}

func writeJSONL(w io.Writer, hits []Hit, truncated bool) error {
	enc := json.NewEncoder(w)
	for _, hit := range hits {
		if err := enc.Encode(hitToExportRow(hit)); err != nil {
			return err
		}
	}
	if truncated {
		var meta exportMeta
		meta.ExportMeta.Truncated = true
		if err := enc.Encode(meta); err != nil {
			return err
		}
	}
	return nil
}

func writeCSV(w io.Writer, hits []Hit, truncated bool) error {
	buf := &bytes.Buffer{}
	writer := csv.NewWriter(buf)
	if err := writer.Write(ExportCSVColumns()); err != nil {
		return err
	}
	for _, hit := range hits {
		if err := writer.Write(hitToCSVRecord(hit)); err != nil {
			return err
		}
	}
	writer.Flush()
	if err := writer.Error(); err != nil {
		return err
	}
	out := buf.String()
	if truncated {
		out += "# truncated: true\n"
	}
	_, err := io.WriteString(w, out)
	return err
}

func hitToExportRow(hit Hit) ExportRow {
	return ExportRow{
		HitID:           hit.ID,
		HitKind:         hit.HitKind,
		Source:          hit.Source,
		Score:           hit.Score,
		SessionID:       hit.SessionID,
		ParentSessionID: hit.ParentSessionID,
		WorkerID:        hit.WorkerID,
		SourceRef:       hit.SourceRef,
		ProjectID:       hit.ProjectID,
		RootID:          hit.RootID,
		ProjectName:     hit.ProjectName,
		Snippet:         hit.Snippet,
		TS:              hit.TS,
		LegID:           hit.LegID,
		Handle:          hit.Handle,
		Tool:            hit.Tool,
		Title:           hit.Title,
		Context:         hit.Context,
		Path:            hit.Path,
		Line:            hit.Line,
		URL:             hit.URL,
		Trust:           hit.Trust,
		Verified:        hit.Verified,
		HintCode:        hit.HintCode,
	}
}

func hitToCSVRecord(hit Hit) []string {
	verified := ""
	if hit.Verified != nil {
		verified = strconv.FormatBool(*hit.Verified)
	}
	line := ""
	if hit.Line > 0 {
		line = strconv.Itoa(hit.Line)
	}
	return []string{
		hit.ID,
		hit.HitKind,
		hit.Source,
		strconv.FormatFloat(hit.Score, 'g', -1, 64),
		hit.ProjectID,
		hit.RootID,
		hit.ProjectName,
		hit.SessionID,
		hit.ParentSessionID,
		hit.WorkerID,
		hit.SourceRef,
		hit.LegID,
		hit.Handle,
		hit.Tool,
		hit.Title,
		hit.Context,
		hit.Path,
		line,
		hit.URL,
		hit.Trust,
		verified,
		hit.HintCode,
		hit.TS,
		hit.Snippet,
	}
}

// ExportContentType returns the MIME type for a format.
func ExportContentType(format string) string {
	switch strings.ToLower(strings.TrimSpace(format)) {
	case ExportFormatJSONL:
		return "application/x-ndjson"
	case ExportFormatCSV:
		return "text/csv"
	case ExportFormatSARIF:
		return "application/sarif+json"
	default:
		return "application/octet-stream"
	}
}

// ExportFilename builds a download filename for an export.
func ExportFilename(format, scope string, ts string) string {
	scope = strings.TrimSpace(scope)
	if scope == "" {
		scope = "global"
	}
	ts = strings.TrimSpace(ts)
	if ts == "" {
		ts = "export"
	}
	ts = strings.NewReplacer(":", "-", ".", "-").Replace(ts)
	ext := strings.ToLower(strings.TrimSpace(format))
	return fmt.Sprintf("search-export-%s-%s.%s", scope, ts, ext)
}

const (
	ExportFormatJSONL = "jsonl"
	ExportFormatCSV   = "csv"
	ExportFormatSARIF = "sarif"
)

var (
	exportFormats    = []string{ExportFormatJSONL, ExportFormatCSV, ExportFormatSARIF}
	exportCSVColumns = []string{
		"hit_id", "kind", "source", "score", "project_id", "root_id", "project_name", "session_id",
		"parent_session_id", "worker_id", "source_ref", "leg_id", "handle", "tool", "title", "context",
		"path", "line", "url", "trust", "verified", "hint_code", "timestamp", "snippet",
	}
)

func ExportFormats() []string {
	return append([]string(nil), exportFormats...)
}

func ExportCSVColumns() []string {
	return append([]string(nil), exportCSVColumns...)
}

// PlanSARIFScoped reports whether the compiled plan may export SARIF (scan findings only).
func PlanSARIFScoped(plan *RoutedPlan) bool {
	if plan == nil || plan.Code != nil || plan.Store == nil {
		return false
	}
	scanScoped := false
	for _, f := range plan.Interpretation.Filters {
		if f.Negated {
			continue
		}
		switch {
		case f.Field == "kind" && strings.EqualFold(f.Value, scanKind),
			f.Field == "shape" && strings.EqualFold(f.Value, "artifact"):
			scanScoped = true
		case f.Field == "kind":
			return false
		}
	}
	if len(plan.Interpretation.FTSTerms) > 0 && !scanScoped {
		return false
	}
	return scanScoped
}

// HitsSARIFEligible reports whether every hit can map to a scan finding.
func HitsSARIFEligible(hits []Hit) bool {
	for _, hit := range hits {
		if !hitSARIFEligible(hit) {
			return false
		}
	}
	return true
}

func hitSARIFEligible(hit Hit) bool {
	kind := strings.TrimSpace(hit.HitKind)
	source := strings.TrimSpace(hit.Source)
	return kind == HitKindFinding || source == SourceFinding
}
