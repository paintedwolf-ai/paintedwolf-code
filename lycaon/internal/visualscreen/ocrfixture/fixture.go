// Package ocrfixture renders deterministic text rasters for exercising text
// recognition: tests and the engine self-check draw the same images from the
// bundled Inter face.
package ocrfixture

import (
	"bytes"
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"image/png"

	"github.com/lycaon/lycaon/internal/fonts"
	"golang.org/x/image/font"
	"golang.org/x/image/font/opentype"
	"golang.org/x/image/math/fixed"
)

// Options describes one rendered fixture.
type Options struct {
	// Lines are drawn top to bottom, left aligned.
	Lines []string
	// PointSize is the type size in points at 72 dpi; zero uses 28.
	PointSize float64
	// Padding is the white margin around the text block in pixels; zero uses 24.
	Padding int
	// QuarterTurns rotates the finished raster clockwise by 90° steps.
	QuarterTurns int
	// Inverted draws light text on a dark background.
	Inverted bool
}

// Line is where a rendered line landed, normalized to the raster.
type Line struct {
	Text        string
	BoundingBox [4]float64
}

// Render draws the options and reports each line's box before rotation is
// applied to the boxes as well, so callers can compare recognized geometry.
func Render(opts Options) (*image.RGBA, []Line, error) {
	_, inter, err := fonts.Parsed()
	if err != nil {
		return nil, nil, err
	}
	size := opts.PointSize
	if size <= 0 {
		size = 28
	}
	face, err := opentype.NewFace(inter, &opentype.FaceOptions{Size: size, DPI: 72, Hinting: font.HintingFull})
	if err != nil {
		return nil, nil, fmt.Errorf("inter face: %w", err)
	}
	defer func() { _ = face.Close() }()
	padding := opts.Padding
	if padding <= 0 {
		padding = 24
	}
	metrics := face.Metrics()
	lineHeight := (metrics.Ascent + metrics.Descent).Ceil() + metrics.Height.Ceil()/4
	drawer := &font.Drawer{Face: face}
	width := 0
	for _, line := range opts.Lines {
		if w := drawer.MeasureString(line).Ceil(); w > width {
			width = w
		}
	}
	width += 2 * padding
	height := lineHeight*len(opts.Lines) + 2*padding
	if len(opts.Lines) == 0 {
		width, height = 2*padding, 2*padding
	}

	bg, fg := color.White, color.Black
	if opts.Inverted {
		bg, fg = color.Black, color.White
	}
	img := image.NewRGBA(image.Rect(0, 0, width, height))
	draw.Draw(img, img.Bounds(), image.NewUniform(bg), image.Point{}, draw.Src)
	drawer.Dst = img
	drawer.Src = image.NewUniform(fg)

	lines := make([]Line, 0, len(opts.Lines))
	for i, text := range opts.Lines {
		top := padding + i*lineHeight
		baseline := top + metrics.Ascent.Ceil()
		drawer.Dot = fixed.P(padding, baseline)
		drawer.DrawString(text)
		w := drawer.MeasureString(text).Ceil()
		box := [4]float64{
			float64(padding) / float64(width),
			float64(top) / float64(height),
			float64(w) / float64(width),
			float64(metrics.Ascent.Ceil()+metrics.Descent.Ceil()) / float64(height),
		}
		lines = append(lines, Line{Text: text, BoundingBox: rotateBox(box, opts.QuarterTurns)})
	}
	return Rotate(img, opts.QuarterTurns), lines, nil
}

// Rotate turns a raster clockwise by quarter turns.
func Rotate(src *image.RGBA, quarterTurns int) *image.RGBA {
	turns := ((quarterTurns % 4) + 4) % 4
	if turns == 0 {
		return src
	}
	b := src.Bounds()
	w, h := b.Dx(), b.Dy()
	var dst *image.RGBA
	if turns%2 == 1 {
		dst = image.NewRGBA(image.Rect(0, 0, h, w))
	} else {
		dst = image.NewRGBA(image.Rect(0, 0, w, h))
	}
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			c := src.RGBAAt(b.Min.X+x, b.Min.Y+y)
			switch turns {
			case 1:
				dst.SetRGBA(h-1-y, x, c)
			case 2:
				dst.SetRGBA(w-1-x, h-1-y, c)
			case 3:
				dst.SetRGBA(y, w-1-x, c)
			}
		}
	}
	return dst
}

// rotateBox maps a normalized box through the same clockwise quarter turns.
func rotateBox(box [4]float64, quarterTurns int) [4]float64 {
	turns := ((quarterTurns % 4) + 4) % 4
	x, y, w, h := box[0], box[1], box[2], box[3]
	switch turns {
	case 1:
		return [4]float64{1 - y - h, x, h, w}
	case 2:
		return [4]float64{1 - x - w, 1 - y - h, w, h}
	case 3:
		return [4]float64{y, 1 - x - w, h, w}
	default:
		return box
	}
}

// PNG encodes a raster.
func PNG(img image.Image) ([]byte, error) {
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}
