// Package contactsheet lays frames out in a labeled grid, one image a model or a reader can
// take in at once: a recording's key moments, or the frames sampled from a video.
package contactsheet

import (
	"bytes"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"math"

	"github.com/lycaon/lycaon/internal/fonts"
	"golang.org/x/image/draw"
	"golang.org/x/image/font"
	"golang.org/x/image/font/opentype"
	"golang.org/x/image/math/fixed"
)

// Cell is one frame and the label printed above it.
type Cell struct {
	Image image.Image
	Label string
}

// Options bound a sheet. MaxEdge caps both dimensions; Columns caps the grid width.
type Options struct {
	MaxEdge int
	Columns int
}

const (
	gutter     = 8
	labelBand  = 22
	labelSize  = 13
	labelInset = 6
)

var (
	sheetBackground = color.RGBA{R: 24, G: 26, B: 31, A: 255}
	labelInk        = color.RGBA{R: 236, G: 238, B: 242, A: 255}
)

// Compose renders cells in reading order into a PNG no larger than MaxEdge on either side.
func Compose(cells []Cell, opts Options) ([]byte, error) {
	if len(cells) == 0 {
		return nil, fmt.Errorf("contact sheet has no cells")
	}
	cols := min(max(opts.Columns, 1), len(cells))
	rows := (len(cells) + cols - 1) / cols
	first := cells[0].Image.Bounds()
	aspect := float64(first.Dy()) / float64(max(first.Dx(), 1))
	// Fit the grid inside the edge budget, never enlarging a frame.
	cellW := min(first.Dx(), (opts.MaxEdge-gutter*(cols+1))/cols)
	if h := float64(opts.MaxEdge-gutter*(rows+1)-labelBand*rows) / float64(rows); float64(cellW)*aspect > h {
		cellW = int(h / aspect)
	}
	if cellW < 16 {
		return nil, fmt.Errorf("contact sheet of %d cells does not fit %dpx", len(cells), opts.MaxEdge)
	}
	cellH := int(math.Round(float64(cellW) * aspect))
	width := cols*cellW + gutter*(cols+1)
	height := rows*(cellH+labelBand) + gutter*(rows+1)
	sheet := image.NewRGBA(image.Rect(0, 0, width, height))
	draw.Draw(sheet, sheet.Bounds(), &image.Uniform{C: sheetBackground}, image.Point{}, draw.Src)
	face, err := labelFace()
	if err != nil {
		return nil, err
	}
	for i, cell := range cells {
		x := gutter + (i%cols)*(cellW+gutter)
		y := gutter + (i/cols)*(cellH+labelBand+gutter)
		drawLabel(sheet, face, cell.Label, x, y, cellW)
		dst := image.Rect(x, y+labelBand, x+cellW, y+labelBand+cellH)
		draw.ApproxBiLinear.Scale(sheet, dst, cell.Image, cell.Image.Bounds(), draw.Src, nil)
	}
	var out bytes.Buffer
	if err := png.Encode(&out, sheet); err != nil {
		return nil, fmt.Errorf("encode contact sheet: %w", err)
	}
	return out.Bytes(), nil
}

func labelFace() (font.Face, error) {
	_, inter, err := fonts.Parsed()
	if err != nil {
		return nil, err
	}
	return opentype.NewFace(inter, &opentype.FaceOptions{Size: labelSize, DPI: 72, Hinting: font.HintingFull})
}

// drawLabel prints a label on the band above a cell, eliding what does not fit.
func drawLabel(dst *image.RGBA, face font.Face, label string, x, y, width int) {
	d := &font.Drawer{Dst: dst, Src: &image.Uniform{C: labelInk}, Face: face}
	limit := fixed.I(width - 2*labelInset)
	runes := []rune(label)
	for len(runes) > 0 && d.MeasureString(string(runes)) > limit {
		runes = runes[:len(runes)-1]
		if len(runes) > 0 && d.MeasureString(string(runes)+"…") <= limit {
			runes = append(runes, '…')
			break
		}
	}
	ascent := face.Metrics().Ascent.Ceil()
	d.Dot = fixed.P(x+labelInset, y+(labelBand+ascent)/2-1)
	d.DrawString(string(runes))
}

// ClockLabel labels a moment in a recording as minutes, seconds, and tenths, so a sheet's
// cells and any prose that cites them read the same.
func ClockLabel(ms float64) string {
	tenths := int(ms/100 + 0.5)
	return fmt.Sprintf("%d:%02d.%d", tenths/600, (tenths/10)%60, tenths%10)
}
