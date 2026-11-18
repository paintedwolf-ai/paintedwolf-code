package workercompletion

import (
	"github.com/lycaon/lycaon/internal/toolrejection"
	workertools "github.com/lycaon/lycaon/internal/tools/native/workercontrol"
)

// ResultNotEarlierCode refuses a report citing a result that is not one of
// the worker's own settled results.
const ResultNotEarlierCode = "COMPLETE_LEG_RESULT_NOT_EARLIER"

// CheckCitedResults admits cited_results only from earlier, the handles of
// the worker's settled rounds.
func CheckCitedResults(cited, earlier []string) error {
	known := make(map[string]bool, len(earlier))
	for _, h := range earlier {
		known[h] = true
	}
	var unknown []string
	for _, h := range cited {
		if !known[h] {
			unknown = append(unknown, h)
		}
	}
	if len(unknown) == 0 {
		return nil
	}
	return &toolrejection.ToolReject{Code: ResultNotEarlierCode, Data: map[string]any{
		"tool": workertools.CompleteLegTool, "unknown_cited_results": unknown,
		"earlier_results": append([]string{}, earlier...),
	}}
}
