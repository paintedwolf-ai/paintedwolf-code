//go:build (!darwin && !linux) || (darwin && !cgo)

package visualscreen

import (
	"context"
)

type unavailableOCR struct{}

// NewDefaultOCREngine reports OCR as unavailable: macOS links Apple Vision
// through cgo and Linux runs the bundled engine; no other platform has one.
func NewDefaultOCREngine() OCREngine {
	return unavailableOCR{}
}

func (unavailableOCR) RecognizeText(context.Context, string, []byte) ([]TextSpan, error) {
	return nil, ErrOCRUnavailable
}

// DefaultOCRBackend reports that this platform has no recognizer.
func DefaultOCRBackend() (string, error) {
	return "", ErrOCRUnavailable
}
