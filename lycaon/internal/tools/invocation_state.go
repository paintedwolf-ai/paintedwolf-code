package tools

// BeginInvocation clears inherited reviews and opens one-call capability state.
func (tc *ToolContext) BeginInvocation() {
	tc.Execution.ProcessControl, tc.Execution.HostExecution = false, false
	tc.Execution.executionPermit = nil
	tc.Effects.Secrets = nil
	tc.Files.ApprovedFileAccess = nil
	tc.Files.PreparedFileAccess = nil
	tc.Files.PolicyWriteGrants = nil
	tc.Execution.ProcessReview = nil
	tc.Files.FileChangeReview = nil
	tc.Files.contentReviews = &contentReviews{}
}
