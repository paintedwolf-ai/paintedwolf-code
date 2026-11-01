package invocation

import (
	"encoding/json"

	"github.com/lycaon/lycaon/pkg/api"
)

type executionSelection struct {
	ids string
}

func selectActivityExecutions(activities []api.CommandActivity, id string) (executionSelection, error) {
	if id == "" {
		return executionSelection{}, nil
	}
	for _, activity := range activities {
		if activity.ID != id {
			continue
		}
		if len(activity.ExecutionIDs) == 0 {
			return executionSelection{}, nil
		}
		ids, err := json.Marshal(activity.ExecutionIDs)
		return executionSelection{ids: string(ids)}, err
	}
	return executionSelection{}, ErrProgramNotFound
}

const activityExecutionFilter = `AND (?='' OR EXISTS (
 SELECT 1 FROM invocation_program_executions a
 WHERE a.program_id=e.program_id AND a.id IN (SELECT value FROM json_each(COALESCE(NULLIF(?,''),'[]')))
 AND ((a.operation_id='program &' AND a.id=e.id)
 OR (a.operation_id<>'program &' AND a.node_id=e.node_id
 AND a.operation_id=e.operation_id AND a.scope_json=e.scope_json))))`
