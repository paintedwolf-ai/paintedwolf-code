package report

import (
	"github.com/johnfercher/maroto/v2/pkg/consts/fontstyle"
	"github.com/phpdave11/gofpdf"
)

// measurer measures strings against the same font metrics the page is drawn
// with, so a width computed here and a width drawn there agree exactly. This
// package breaks its own lines; those breaks are only sound if the two match.
type measurer struct {
	pdf        *gofpdf.Fpdf
	cache      map[widthKey]float64
	spaceCache map[styleKey]float64
}

type styleKey struct {
	family string
	style  fontstyle.Type
	size   float64
}

type widthKey struct {
	styleKey
	text string
}

func newMeasurer() (*measurer, error) {
	pdf := gofpdf.New("P", "mm", "A4", "")
	if err := registerFaces(pdf); err != nil {
		return nil, err
	}
	pdf.SetFont(familySans, "", sizeBody)
	return &measurer{
		pdf:        pdf,
		cache:      map[widthKey]float64{},
		spaceCache: map[styleKey]float64{},
	}, nil
}

// width returns the rendered width of text in millimetres.
func (m *measurer) width(text string, k styleKey) float64 {
	if text == "" {
		return 0
	}
	key := widthKey{styleKey: k, text: text}
	if w, ok := m.cache[key]; ok {
		return w
	}
	m.pdf.SetFont(k.family, string(k.style), k.size)
	w := m.pdf.GetStringWidth(text)
	m.cache[key] = w
	return w
}

// spaceWidth returns the width of a single inter-word space.
func (m *measurer) spaceWidth(k styleKey) float64 {
	if w, ok := m.spaceCache[k]; ok {
		return w
	}
	m.pdf.SetFont(k.family, string(k.style), k.size)
	w := m.pdf.GetStringWidth(" ")
	m.spaceCache[k] = w
	return w
}

// fontHeight is the baseline advance for a font size, in millimetres.
func (m *measurer) fontHeight(sizePt float64) float64 { return sizePt * ptToMM }
