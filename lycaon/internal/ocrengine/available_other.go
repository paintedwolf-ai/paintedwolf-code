//go:build !darwin && !linux

package ocrengine

// Available reports whether this device can recognize text in images: no other
// platform has a recognizer.
func Available() error {
	return &UnavailableError{Reason: ReasonUnsupportedPlatform}
}
