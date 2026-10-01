package visual

import (
	"bytes"
	"fmt"
	"image"
	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"
	"strings"

	_ "golang.org/x/image/webp"
)

// sizedRasterMimes are the raster formats whose pixel size the store reads.
var sizedRasterMimes = map[string]struct{}{
	"image/png":  {},
	"image/jpeg": {},
	"image/gif":  {},
	"image/webp": {},
}

// rasterSize reads pixel dimensions from a raster header. Other media carry
// no size; a sized raster mime whose header does not decode is refused.
func rasterSize(mime string, raw []byte) (width, height int, err error) {
	if _, ok := sizedRasterMimes[strings.ToLower(strings.TrimSpace(mime))]; !ok {
		return 0, 0, nil
	}
	cfg, _, err := image.DecodeConfig(bytes.NewReader(raw))
	if err != nil || cfg.Width <= 0 || cfg.Height <= 0 {
		return 0, 0, fmt.Errorf("artifact declared %q is not a decodable raster image", mime)
	}
	return cfg.Width, cfg.Height, nil
}
