package invocation

import (
	"encoding/json"
	"slices"

	"github.com/lycaon/lycaon/pkg/api"
)

type executionSelection struct {
	ids string
}

func findActivity(activities []api.CommandActivity, id string) *api.CommandActivity {
	for i := range activities {
		if activities[i].ID == id {
			return &activities[i]
		}
	}
	for i := range activities {
		if slices.Contains(activities[i].ExecutionIDs, id) {
			return &activities[i]
		}
	}
	return nil
}

func selectActivityExecutions(activities []api.CommandActivity, id string) (executionSelection, error) {
	if id == "" {
		return executionSelection{}, nil
	}
	activity := findActivity(activities, id)
	if activity == nil {
		return executionSelection{}, ErrProgramNotFound
	}
	if len(activity.ExecutionIDs) == 0 {
		return executionSelection{}, nil
	}
	ids, err := json.Marshal(activity.ExecutionIDs)
	return executionSelection{ids: string(ids)}, err
}

const activityExecutionFilter = `AND (?='' OR EXISTS (
 SELECT 1 FROM invocation_program_executions a
 WHERE a.program_id=e.program_id AND a.id IN (SELECT value FROM json_each(COALESCE(NULLIF(?,''),'[]')))
 AND ((a.operation_id='program &' AND a.id=e.id)
 OR (a.operation_id<>'program &' AND a.node_id=e.node_id
 AND a.operation_id=e.operation_id AND a.scope_json=e.scope_json))))`
