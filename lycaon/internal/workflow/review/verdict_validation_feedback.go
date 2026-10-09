package review

import (
	"maps"

	"github.com/lycaon/lycaon/internal/tools"
)

func verdictInvalidDetails(outline string, err error) map[string]any {
	data := map[string]any{
		"reason":        err.Error(),
		"expected_call": describeVerdictCall(outline),
	}
	if rejection := tools.AsToolReject(err); rejection != nil {
		maps.Copy(data, rejection.Data)
		data["issues"] = err.Error()
	}
	return data
}
