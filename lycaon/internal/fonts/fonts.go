package fonts

import (
	"embed"
	"fmt"
	"sync"

	"golang.org/x/image/font/opentype"
)

//go:embed *.ttf
var faceFS embed.FS

var (
	parseOnce sync.Once
	jbFont    *opentype.Font
	interFont *opentype.Font
	parseErr  error
)

// ReadFace returns raw TTF bytes for a bundled font file.
func ReadFace(name string) ([]byte, error) {
	b, err := faceFS.ReadFile(name)
	if err != nil {
		return nil, fmt.Errorf("read face %s: %w", name, err)
	}
	return b, nil
}

// Parsed returns the parsed JetBrains Mono and Inter OpenType font descriptors.
func Parsed() (*opentype.Font, *opentype.Font, error) {
	parseOnce.Do(func() {
		jbBytes, err := ReadFace("JetBrainsMono-Regular.ttf")
		if err != nil {
			parseErr = err
			return
		}
		interBytes, err := ReadFace("Inter-Regular.ttf")
		if err != nil {
			parseErr = err
			return
		}
		jb, err := opentype.Parse(jbBytes)
		if err != nil {
			parseErr = fmt.Errorf("parse JetBrains Mono: %w", err)
			return
		}
		inter, err := opentype.Parse(interBytes)
		if err != nil {
			parseErr = fmt.Errorf("parse Inter: %w", err)
			return
		}
		jbFont = jb
		interFont = inter
	})
	return jbFont, interFont, parseErr
}
