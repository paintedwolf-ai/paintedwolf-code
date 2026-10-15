//go:build linux

package ocrengine

// Available reports whether this device can recognize text in images: on Linux
// that is the verified bundled engine.
func Available() error {
	_, err := Resolve()
	return err
}
