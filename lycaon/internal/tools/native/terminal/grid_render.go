package terminal

import (
	"bytes"
	"image"
	"image/color"
	"image/draw"
	"image/png"
	"unicode/utf8"

	"github.com/lycaon/lycaon/internal/bgprocess"
	"github.com/lycaon/lycaon/internal/fonts"
	"github.com/lycaon/lycaon/internal/llm/providerwire"
	"github.com/lycaon/lycaon/internal/visual"
	"golang.org/x/image/font"
	"golang.org/x/image/font/basicfont"
	"golang.org/x/image/font/opentype"
	"golang.org/x/image/math/fixed"
)

var (
	gridFg       = color.RGBA{R: 230, G: 230, B: 230, A: 255}
	gridBg       = color.RGBA{R: 18, G: 18, B: 18, A: 255}
	gridCursorBg = color.RGBA{R: 80, G: 140, B: 220, A: 255}
)

func renderTerminalGridPNG(screen bgprocess.ScreenSnapshot) (mime string, pngBytes []byte, width, height int, err error) {
	cols := screen.Cols
	rows := screen.Rows
	if cols <= 0 || rows <= 0 || len(screen.Lines) == 0 {
		return "", nil, 0, 0, nil
	}

	jbFont, interFont, fontErr := fonts.Parsed()
	if fontErr == nil {
		jbFace, jbErr := opentype.NewFace(jbFont, &opentype.FaceOptions{
			Size:    13,
			DPI:     72,
			Hinting: font.HintingFull,
		})
		interFace, interErr := opentype.NewFace(interFont, &opentype.FaceOptions{
			Size:    13,
			DPI:     72,
			Hinting: font.HintingFull,
		})
		if jbErr == nil && interErr == nil {
			return renderVectorGrid(screen, jbFace, interFace, cols, rows)
		}
	}

	return renderBitmapFallback(screen, cols, rows)
}

func renderVectorGrid(
	screen bgprocess.ScreenSnapshot,
	jbFace, interFace font.Face,
	cols, rows int,
) (string, []byte, int, int, error) {
	ascent := jbFace.Metrics().Ascent.Ceil()
	descent := jbFace.Metrics().Descent.Ceil()
	cellH := ascent + descent
	adv, _ := jbFace.GlyphAdvance('M')
	cellW := adv.Ceil()
	pad := 4
	width := cols*cellW + pad*2
	height := rows*cellH + pad*2

	img := image.NewRGBA(image.Rect(0, 0, width, height))
	draw.Draw(img, img.Bounds(), &image.Uniform{C: gridBg}, image.Point{}, draw.Src)

	jbDrawer := &font.Drawer{Dst: img, Src: image.NewUniform(gridFg), Face: jbFace}
	interDrawer := &font.Drawer{Dst: img, Src: image.NewUniform(gridFg), Face: interFace}
	cursorJBDrawer := &font.Drawer{Dst: img, Src: image.NewUniform(gridBg), Face: jbFace}
	cursorInterDrawer := &font.Drawer{Dst: img, Src: image.NewUniform(gridBg), Face: interFace}

	for y := 0; y < rows && y < len(screen.Lines); y++ {
		line := screen.Lines[y]
		runes := []rune(line)
		for x := 0; x < cols; x++ {
			ch := ' '
			if x < len(runes) {
				ch = runes[x]
			}
			if !utf8.ValidRune(ch) || ch < ' ' {
				ch = ' '
			}

			px := pad + x*cellW
			py := pad + y*cellH
			isCursor := x == screen.CursorCol && y == screen.CursorRow

			if isCursor {
				cellRect := image.Rect(px, py, px+cellW, py+cellH)
				draw.Draw(img, cellRect, &image.Uniform{C: gridCursorBg}, image.Point{}, draw.Src)
			}

			if ch <= ' ' {
				continue
			}

			jd, id := jbDrawer, interDrawer
			if isCursor {
				jd, id = cursorJBDrawer, cursorInterDrawer
			}

			dot := fixed.P(px, py+ascent)
			if _, ok := jbFace.GlyphAdvance(ch); ok {
				jd.Dot = dot
				jd.DrawString(string(ch))
			} else if _, ok := interFace.GlyphAdvance(ch); ok {
				id.Dot = dot
				id.DrawString(string(ch))
			} else {
				jd.Dot = dot
				jd.DrawString("▯")
			}
		}
	}

	return encodePNG(img, width, height)
}

func renderBitmapFallback(screen bgprocess.ScreenSnapshot, cols, rows int) (string, []byte, int, int, error) {
	face := basicfont.Face7x13
	cellW := face.Width
	cellH := face.Height
	pad := 4
	width := cols*cellW + pad*2
	height := rows*cellH + pad*2
	img := image.NewRGBA(image.Rect(0, 0, width, height))
	draw.Draw(img, img.Bounds(), &image.Uniform{C: gridBg}, image.Point{}, draw.Src)

	drawer := &font.Drawer{
		Dst:  img,
		Src:  image.NewUniform(gridFg),
		Face: face,
	}

	for y := 0; y < rows && y < len(screen.Lines); y++ {
		line := screen.Lines[y]
		runes := []rune(line)
		for x := 0; x < cols; x++ {
			ch := ' '
			if x < len(runes) {
				ch = runes[x]
			}
			if !utf8.ValidRune(ch) || ch < ' ' {
				ch = ' '
			}
			px := pad + x*cellW
			py := pad + y*cellH
			if x == screen.CursorCol && y == screen.CursorRow {
				cellRect := image.Rect(px, py, px+cellW, py+cellH)
				draw.Draw(img, cellRect, &image.Uniform{C: gridCursorBg}, image.Point{}, draw.Src)
			}
			drawer.Dot = fixed.P(px, py+face.Ascent)
			drawer.DrawString(string(ch))
		}
	}

	return encodePNG(img, width, height)
}

func encodePNG(img image.Image, width, height int) (string, []byte, int, int, error) {
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		return "", nil, 0, 0, err
	}
	norm, err := providerwire.NormalizeImageBytes(buf.Bytes(), "image/png", visual.MaxRasterBytes())
	if err != nil {
		return "", nil, 0, 0, err
	}
	return norm.Mime, append([]byte(nil), norm.Bytes...), width, height, nil
}
