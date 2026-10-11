package runstate

import "github.com/lycaon/lycaon/pkg/api"

const ReportNotAcceptedFailureCode = "REPORT_NOT_ACCEPTED"

func ReportNotAcceptedRun(run *api.WorkflowRun) bool {
	return run != nil && run.Status == api.WorkflowRunStatusFailed &&
		run.Failure != nil && run.Failure.Code == ReportNotAcceptedFailureCode
}
