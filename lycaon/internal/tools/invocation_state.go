package tools

// BeginInvocation clears inherited reviews and opens one-call capability state.
func (tc *ToolContext) BeginInvocation() {
	tc.ProcessControl, tc.HostExecution = false, false
	tc.executionPermit = nil
	tc.Secrets = nil
	tc.ApprovedFileAccess = nil
	tc.PreparedFileAccess = nil
	tc.PolicyWriteGrants = nil
	tc.ProcessReview = nil
	tc.FileChangeReview = nil
	tc.contentReviews = &contentReviews{}
}
