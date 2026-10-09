package session

func workflowDomainFixture(v any) *WorkflowDomains {
	d := &WorkflowDomains{}
	d.Runs, _ = v.(WorkflowRuns)
	d.Policy, _ = v.(WorkflowPolicy)
	d.Ambient, _ = v.(WorkflowAmbient)
	d.Blueprints, _ = v.(WorkflowBlueprints)
	d.Batch, _ = v.(WorkflowBatch)
	d.Slash, _ = v.(WorkflowSlash)
	d.Requests, _ = v.(WorkflowRequests)
	d.Feedback, _ = v.(WorkflowFeedback)
	d.Transcript, _ = v.(WorkflowTranscript)
	d.Asks, _ = v.(WorkflowAsks)
	d.Fanout, _ = v.(WorkflowFanout)
	d.Phases, _ = v.(WorkflowPhases)
	d.Reports, _ = v.(WorkflowReports)
	d.Recovery, _ = v.(WorkflowRecovery)
	d.Cleanup, _ = v.(WorkflowCleanup)
	return d
}
