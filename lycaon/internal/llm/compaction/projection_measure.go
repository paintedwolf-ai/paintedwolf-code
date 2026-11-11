package compaction

import "github.com/lycaon/lycaon/pkg/api"

// ProjectionDoesNotGrow validates optional enrichment in both supported measurements.
func ProjectionDoesNotGrow(info SessionInfo, before, after []api.Message) (bool, error) {
	oldRows, newRows := ContextMessagesFromAPI(before), ContextMessagesFromAPI(after)
	measured, err := measureCompactionPair(info, oldRows, newRows)
	if err != nil {
		return false, err
	}
	return EstimateMessagesTokens(newRows) <= EstimateMessagesTokens(oldRows) && measured.After <= measured.Before, nil
}
