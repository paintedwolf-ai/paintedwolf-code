package invocation

import (
	"strings"

	"github.com/lycaon/lycaon/pkg/api"
)

type executionSelection struct {
	sql  string
	args []any
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
		marks := make([]string, 0, len(activity.ExecutionIDs))
		args := make([]any, 0, len(activity.ExecutionIDs))
		for _, id := range activity.ExecutionIDs {
			marks = append(marks, "?")
			args = append(args, id)
		}
		return executionSelection{sql: `AND EXISTS (SELECT 1 FROM invocation_program_executions a WHERE a.program_id=e.program_id AND a.id IN (` + strings.Join(marks, ",") + `) AND ((a.operation_id='program &' AND a.id=e.id) OR (a.operation_id<>'program &' AND a.node_id=e.node_id AND a.operation_id=e.operation_id AND a.scope_json=e.scope_json)))`, args: args}, nil
	}
	return executionSelection{}, ErrProgramNotFound
}
