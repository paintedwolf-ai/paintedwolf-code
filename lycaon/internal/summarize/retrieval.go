package summarize

import "github.com/lycaon/lycaon/internal/textrank"

// taskFieldScores uses shared source-aware ranking.
func taskFieldScores(task string, docs [][]textrank.Field) []float64 {
	return textrank.FieldScores(task, docs, textrank.CodeOptions())
}

// taskQueryTerms returns unique analyzed task terms.
func taskQueryTerms(task string) []string {
	return textrank.AnalyzeQuery(task, true, true)
}
