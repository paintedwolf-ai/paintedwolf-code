package git

import (
	"github.com/lycaon/lycaon/internal/tools/surveyjson"
	"unicode/utf8"
)

const operationResultPaths = 80
const operationDiagnosticsBytes = 16 << 10

type operationStateJSON struct {
	RepositoryState
	ConflictsTotal     int  `json:"conflicts_total"`
	ConflictsTruncated bool `json:"conflicts_truncated"`
}

func operationStateForJSON(state RepositoryState) operationStateJSON {
	result := operationStateJSON{RepositoryState: state, ConflictsTotal: len(state.Conflicts)}
	if len(state.Conflicts) > operationResultPaths {
		result.Conflicts = state.Conflicts[:operationResultPaths]
		result.ConflictsTruncated = true
	}
	return result
}

// MarshalOperationResult bounds lists and diagnostics while preserving state summaries.
func MarshalOperationResult(result OperationResult) (string, error) {
	pathsTotal := len(result.Paths)
	if len(result.Paths) > operationResultPaths {
		result.Paths = result.Paths[:operationResultPaths]
	}
	diagnosticsTruncated := len(result.Diagnostics) > operationDiagnosticsBytes
	if diagnosticsTruncated {
		result.Diagnostics = result.Diagnostics[:operationDiagnosticsBytes]
		for !utf8.ValidString(result.Diagnostics) && len(result.Diagnostics) > 0 {
			result.Diagnostics = result.Diagnostics[:len(result.Diagnostics)-1]
		}
	}
	raw, err := surveyjson.Marshal(struct {
		OperationResult
		Before               operationStateJSON `json:"before"`
		After                operationStateJSON `json:"after"`
		PathsTotal           int                `json:"reviewed_paths_total"`
		PathsTruncated       bool               `json:"reviewed_paths_truncated"`
		DiagnosticsTruncated bool               `json:"diagnostics_truncated"`
	}{result, operationStateForJSON(result.Before), operationStateForJSON(result.After), pathsTotal, pathsTotal > operationResultPaths, diagnosticsTruncated})
	return string(raw), err
}
