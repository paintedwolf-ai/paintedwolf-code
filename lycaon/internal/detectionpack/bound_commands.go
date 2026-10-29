package detectionpack

// boundCommandEvents consumes the same decoded argv used by native/external
// dispatch. Wrapper identity normalization reuses the existing execution-plane
// projection and does not tokenize or execute the canonical display strings.
func boundCommandEvents(in ActionObservation) []Event {
	groups := make([]Event, 0, len(in.BoundStages))
	for _, stage := range in.BoundStages {
		resolved := resolveBoundStage(stage.Argv)
		event := Event{Tool: in.Tool, CommandLine: stage.CommandLine, PipelineCommandLine: stage.PipelineCommandLine, Argv: append([]string(nil), stage.Argv...), ProjectDir: in.ProjectDir, SessionID: in.SessionID, ActionID: stage.ExecutionID, Contained: "false", EgressMode: in.Boundary.Egress}
		if in.Boundary.FSJailed && !in.Boundary.DirectIP && len(in.Sockets) == 0 {
			event.Contained = "true"
		}
		if resolved.image != "" {
			event.Image = []string{resolved.image}
		}
		if action := ApiActionFromArgv(resolved.image, resolved.argv); action != "" {
			event.ApiAction = []string{action}
		}
		groups = append(groups, event)
	}
	return groups
}
