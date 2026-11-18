package search

const (
	TableEvidenceIndex = "evidence_index"
	TableMessagesFTS   = "messages_fts"
	TableEvidenceFTS   = "evidence_fts"
)

// DSLDefaultScope is the query scope when no project: filter is present.
const DSLDefaultScope = "global"

var (
	evidenceIndexColumns = []string{
		"id",
		"project_id",
		"source",
		"hit_kind",
		"session_id",
		"root_session_id",
		"message_id",
		"source_ref",
		"leg_id",
		"handle",
		"tool",
		"kind",
		"shape",
		"role",
		"path",
		"line",
		"url",
		"snippet",
		"verified",
		"hint_code",
		"check_id",
		"trust",
		"truncated",
		"ts",
		"workflow_run_id",
		"verdict",
		"untrusted",
		"tombstoned",
	}
	evidenceIndexNotNullColumns = []string{"id", "project_id", "source", "hit_kind", "tombstoned"}
	evidenceIndexCompositeIndex = []string{"project_id", "kind", "ts"}
	messagesFTSColumns          = []string{"content", "kind"}
	evidenceFTSColumns          = []string{"snippet", "path", "url"}
)
