package visualscreen

import (
	"image"
	"image/color"
	"strconv"
)

// tesseractTargetEdge is the long edge a small raster is scaled up to before
// recognition; the engine reads letters reliably from about twenty pixels of
// x-height, which screenshots of ordinary text do not reach at native size.
const tesseractTargetEdge = 1200

// tesseractMaxScale bounds the integer upscale so a tiny raster cannot become
// a large one.
const tesseractMaxScale = 4

// tesseractScale picks the integer factor a raster is enlarged by.
func tesseractScale(bounds image.Rectangle) int {
	edge := max(bounds.Dx(), bounds.Dy())
	if edge <= 0 || edge >= tesseractTargetEdge {
		return 1
	}
	scale := (tesseractTargetEdge + edge - 1) / edge
	return min(scale, tesseractMaxScale)
}

// encodePGM renders a raster as an 8-bit binary PGM, enlarged by a whole
// factor with nearest-neighbour sampling so recognized geometry divides back
// exactly. The returned size is the encoded raster's.
func encodePGM(img image.Image, scale int) ([]byte, int, int) {
	if scale < 1 {
		scale = 1
	}
	b := img.Bounds()
	w, h := b.Dx()*scale, b.Dy()*scale
	header := "P5\n" + strconv.Itoa(w) + " " + strconv.Itoa(h) + "\n255\n"
	out := make([]byte, len(header)+w*h)
	copy(out, header)
	pixels := out[len(header):]
	row := make([]byte, b.Dx())
	for y := 0; y < b.Dy(); y++ {
		for x := 0; x < b.Dx(); x++ {
			row[x] = grayOf(img.At(b.Min.X+x, b.Min.Y+y))
		}
		for sy := 0; sy < scale; sy++ {
			line := pixels[((y*scale)+sy)*w:]
			for x, v := range row {
				for sx := 0; sx < scale; sx++ {
					line[x*scale+sx] = v
				}
			}
		}
	}
	return out, w, h
}

// grayOf composites a pixel over white before taking its luminance, so a
// transparent background reads as paper rather than black.
func grayOf(c color.Color) byte {
	r, g, b, a := c.RGBA()
	if a == 0 {
		return 255
	}
	if a < 0xffff {
		r += 0xffff - a
		g += 0xffff - a
		b += 0xffff - a
		r, g, b = min(r, 0xffff), min(g, 0xffff), min(b, 0xffff)
	}
	// Rec. 601 luma on 16-bit channels, reduced to 8 bits.
	return byte((299*r + 587*g + 114*b) / 1000 >> 8) //nolint:gosec // G115 — RGBA channels are at most 0xffff, so the weighted mean shifted by 8 fits in a byte
}
