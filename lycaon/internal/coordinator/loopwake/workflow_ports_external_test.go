package loopwake_test

type workflowFixtureSource interface {
	loopwake.WorkflowRuns
	loopwake.WorkflowApprovals
	loopwake.WorkflowObligations
}

func workflowFixturePorts(source workflowFixtureSource) *loopwake.WorkflowDomains {
	return &loopwake.WorkflowDomains{Runs: source, Approvals: source, Obligations: source}
}
