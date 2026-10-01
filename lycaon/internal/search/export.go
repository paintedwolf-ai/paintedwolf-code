package search

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
