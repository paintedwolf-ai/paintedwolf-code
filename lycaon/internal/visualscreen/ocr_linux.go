//go:build linux

package visualscreen

import (
	"fmt"

	"github.com/lycaon/lycaon/internal/ocrengine"
)

// NewDefaultOCREngine returns the bundled Tesseract engine on Linux. The
// engine resolves and verifies on each recognition, so an installation that
// is repaired later is picked up without a restart.
func NewDefaultOCREngine() OCREngine {
	return newTesseractOCR(ocrengine.Resolve)
}

// DefaultOCRBackend names the verified recognizer, or reports why none resolved.
func DefaultOCRBackend() (string, error) {
	engine, err := ocrengine.Resolve()
	if err != nil {
		return "", fmt.Errorf("%w: %w", ErrOCRUnavailable, err)
	}
	return "tesseract " + engine.Version + " at " + engine.Root, nil
}
