//go:build windows

package exec

// killedBySIGPIPE is Unix-only: Windows has no SIGPIPE — a producer writing to a
// closed pipe gets a write error and exits by its own convention, so there is no
// signal death to classify as benign.
func killedBySIGPIPE(error) bool {
	return false
}
