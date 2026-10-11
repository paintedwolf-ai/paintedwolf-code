package loopwake

type workflowFixtureSource interface {
	WorkflowRuns
	WorkflowApprovals
	WorkflowObligations
}

func workflowFixturePorts(source workflowFixtureSource) *WorkflowDomains {
	return &WorkflowDomains{Runs: source, Approvals: source, Obligations: source}
}
