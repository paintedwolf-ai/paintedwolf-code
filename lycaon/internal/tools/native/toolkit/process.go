package toolkit

// ProcessStopResult is the command_stop and held_stop result. Tools address a
// process by the handle they returned, so the result names it handle.
type ProcessStopResult struct {
	Handle string `json:"handle"`
	// StopRequested acknowledges the request; it does not assert exit.
	StopRequested bool `json:"stop_requested"`
	// Running stays true until the host observes terminal process state.
	Running  bool `json:"running"`
	ExitCode *int `json:"exit_code,omitempty"`
}
