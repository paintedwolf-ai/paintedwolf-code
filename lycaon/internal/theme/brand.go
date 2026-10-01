package theme

import "sort"

// BrandFieldDefault is the stock lockup plate.
var BrandFieldDefault = Color{R: 0x1c, G: 0x18, B: 0x14, A: 0xff}

// LogomarkVisibility is the closed enum of what a theme may say about the mark.
type LogomarkVisibility string

const (
	// LogomarkShown is the default: the mark paints, in theme colors.
	LogomarkShown LogomarkVisibility = "shown"
	// LogomarkHidden reserves the mark's box and paints nothing in it.
	LogomarkHidden LogomarkVisibility = "hidden"
)

var logomarkVisibilities = map[LogomarkVisibility]bool{
	LogomarkShown:  true,
	LogomarkHidden: true,
}

// ValidLogomarkVisibility reports whether the value is in the closed enum.
func ValidLogomarkVisibility(v LogomarkVisibility) bool {
	return logomarkVisibilities[v]
}

// LogomarkVisibilities returns the sorted closed enum.
func LogomarkVisibilities() []LogomarkVisibility {
	out := make([]LogomarkVisibility, 0, len(logomarkVisibilities))
	for v := range logomarkVisibilities {
		out = append(out, v)
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

// Brand is the resolved brand surface.
type Brand struct {
	Logomark LogomarkVisibility
}

// DefaultBrand returns stock brand settings.
func DefaultBrand() Brand { return Brand{Logomark: LogomarkShown} }
