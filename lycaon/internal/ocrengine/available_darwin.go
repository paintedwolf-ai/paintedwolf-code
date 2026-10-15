//go:build darwin

package ocrengine

// Available reports whether this device can recognize text in images: macOS
// recognizes in-process through Apple Vision and bundles no engine.
func Available() error {
	return nil
}
