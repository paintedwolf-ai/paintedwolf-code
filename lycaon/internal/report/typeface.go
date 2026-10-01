package report

import (
	"github.com/johnfercher/maroto/v2/pkg/consts/fontstyle"
	"github.com/johnfercher/maroto/v2/pkg/core/entity"
	"github.com/lycaon/lycaon/internal/fonts"
	"github.com/phpdave11/gofpdf"
)

// Font family keys used by gofpdf.
const (
	familySans = "inter"
	familyMono = "jetbrainsmono"
)

// face binds a font family and style to its embedded file name.
type face struct {
	family string
	style  fontstyle.Type
	file   string
}

var faces = []face{
	{familySans, fontstyle.Normal, "Inter-Regular.ttf"},
	{familySans, fontstyle.Bold, "Inter-SemiBold.ttf"},
	{familySans, fontstyle.Italic, "Inter-Italic.ttf"},
	{familySans, fontstyle.BoldItalic, "Inter-SemiBoldItalic.ttf"},
	{familyMono, fontstyle.Normal, "JetBrainsMono-Regular.ttf"},
	{familyMono, fontstyle.Bold, "JetBrainsMono-Bold.ttf"},
	{familyMono, fontstyle.Italic, "JetBrainsMono-Italic.ttf"},
	{familyMono, fontstyle.BoldItalic, "JetBrainsMono-BoldItalic.ttf"},
}

func faceBytes(file string) ([]byte, error) {
	return fonts.ReadFace(file)
}

// customFonts is the face set handed to the document builder.
func customFonts() ([]*entity.CustomFont, error) {
	out := make([]*entity.CustomFont, 0, len(faces))
	for _, f := range faces {
		b, err := faceBytes(f.file)
		if err != nil {
			return nil, err
		}
		out = append(out, &entity.CustomFont{Family: f.family, Style: f.style, Bytes: b})
	}
	return out, nil
}

// registerFaces registers font faces with a gofpdf document.
func registerFaces(pdf *gofpdf.Fpdf) error {
	for _, f := range faces {
		b, err := faceBytes(f.file)
		if err != nil {
			return err
		}
		pdf.AddUTF8FontFromBytes(f.family, string(f.style), b)
	}
	return pdf.Error()
}
