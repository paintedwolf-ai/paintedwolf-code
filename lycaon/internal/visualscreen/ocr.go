package visualscreen

import (
	"context"
	"errors"
)

// ErrOCRUnavailable reports a platform without text recognition.
var ErrOCRUnavailable = errors.New("text recognition is unavailable on this platform")

// TextSpan is an extracted or recognized text fragment with its normalized coordinates.
type TextSpan struct {
	Text        string
	Confidence  float64
	BoundingBox [4]float64 // [x, y, width, height] normalized to [0.0, 1.0]
}

// OCREngine recognizes text in raster bytes. It returns ErrOCRUnavailable when
// the platform has no engine, and any other error when recognition failed.
type OCREngine interface {
	RecognizeText(ctx context.Context, mime string, raw []byte) ([]TextSpan, error)
}
