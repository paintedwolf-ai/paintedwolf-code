package providerwire

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"image"
	_ "image/gif"
	_ "image/jpeg"
	"image/png"
	"strings"
	"sync/atomic"

	"github.com/lycaon/lycaon/internal/bytebound"
	"github.com/lycaon/lycaon/internal/observability"
	"github.com/lycaon/lycaon/internal/timelinearchive"
	_ "golang.org/x/image/webp"
)

// MaxImageDimension is the longest edge after downscale-or-reject normalization.
const MaxImageDimension = 2048

// RejectImageDimension is the longest edge above which an image is rejected.
const RejectImageDimension = 4096

var perceiveDropLog = observability.LazyComponent("llm_perceive")

// perceptionImage is the image a model sees for an artifact: the artifact itself, or the
// poster of a recording that is not an image.
func perceptionImage(raw []byte, mime string) ([]byte, string, error) {
	if !strings.EqualFold(strings.TrimSpace(mime), timelinearchive.Mime) {
		return raw, mime, nil
	}
	poster, err := timelinearchive.Poster(raw)
	if err != nil {
		return nil, "", err
	}
	return poster, "image/png", nil
}

// ImageWirePart is a normalized provider image.
type ImageWirePart struct {
	Mime   string
	Base64 string
}

type NormalizedImage struct {
	Bytes []byte
	Mime  string
}

// wireImageMimes is the canonical raster set a provider image block may declare.
var wireImageMimes = map[string]struct{}{
	"image/png":  {},
	"image/jpeg": {},
	"image/gif":  {},
	"image/webp": {},
}

// IsWireImageMime reports whether mime may appear as a provider image-block media type.
func IsWireImageMime(mime string) bool {
	_, ok := wireImageMimes[canonicalImageMime(mime)]
	return ok
}

func canonicalImageMime(mime string) string {
	return strings.ToLower(strings.TrimSpace(mime))
}

// ValidateWireImage derives a safe media type from decoded bytes.
func ValidateWireImage(raw []byte, declaredMime string, max bytebound.Transport) (string, error) {
	mime, _, err := sniffWireImage(raw, declaredMime, max)
	return mime, err
}

func sniffWireImage(raw []byte, declaredMime string, max bytebound.Transport) (string, image.Config, error) {
	if len(raw) == 0 {
		return "", image.Config{}, fmt.Errorf("image bytes are empty")
	}
	// A non-positive bound adds no ceiling.
	if max > 0 && int64(len(raw)) > max.Int64() {
		return "", image.Config{}, fmt.Errorf("image exceeds max bytes (%d)", max.Int64())
	}
	cfg, format, err := image.DecodeConfig(bytes.NewReader(raw))
	if err != nil {
		return "", image.Config{}, fmt.Errorf("payload declared %q is not a decodable raster image", canonicalImageMime(declaredMime))
	}
	mime := "image/" + strings.ToLower(strings.TrimSpace(format))
	if !IsWireImageMime(mime) {
		return "", image.Config{}, fmt.Errorf("image format %q is not accepted on the vision wire", format)
	}
	return mime, cfg, nil
}

// NormalizeImageBytes enforces wire format and dimension bounds.
func NormalizeImageBytes(raw []byte, mime string, max bytebound.Transport) (NormalizedImage, error) {
	canonical, cfg, err := sniffWireImage(raw, mime, max)
	if err != nil {
		return NormalizedImage{}, err
	}
	maxEdge := cfg.Width
	if cfg.Height > maxEdge {
		maxEdge = cfg.Height
	}
	if maxEdge > RejectImageDimension {
		return NormalizedImage{}, fmt.Errorf("image dimension %d exceeds reject threshold %d", maxEdge, RejectImageDimension)
	}
	if maxEdge <= MaxImageDimension {
		return NormalizedImage{Bytes: append([]byte(nil), raw...), Mime: canonical}, nil
	}
	img, _, err := image.Decode(bytes.NewReader(raw))
	if err != nil {
		return NormalizedImage{}, fmt.Errorf("decode image for downscale: %w", err)
	}
	scaled, outMime, err := downscaleImage(img, MaxImageDimension)
	if err != nil {
		return NormalizedImage{}, err
	}
	if max > 0 && int64(len(scaled)) > max.Int64() {
		return NormalizedImage{}, fmt.Errorf("downscaled image still exceeds max bytes")
	}
	return NormalizedImage{Bytes: scaled, Mime: outMime}, nil
}

// UserArtifactWireImage resolves one artifact for provider input.
func UserArtifactWireImage(sessionID, artifactID string) (mime, data string, ok bool) {
	if resolveVisualBytes == nil {
		return "", "", false
	}
	sessionID = strings.TrimSpace(sessionID)
	artifactID = strings.TrimSpace(artifactID)
	if sessionID == "" || artifactID == "" {
		return "", "", false
	}
	raw, declared, ok := resolveVisualBytes(sessionID, artifactID)
	if !ok || len(raw) == 0 {
		return "", "", false
	}
	raw, declared, err := perceptionImage(raw, declared)
	if err != nil {
		return "", "", false
	}
	normalized, err := NormalizeImageBytes(raw, declared, wireImageCeiling())
	if err != nil || !IsWireImageMime(normalized.Mime) {
		return "", "", false
	}
	return normalized.Mime, base64.StdEncoding.EncodeToString(normalized.Bytes), true
}

func downscaleImage(src image.Image, maxEdge int) ([]byte, string, error) {
	b := src.Bounds()
	w, h := b.Dx(), b.Dy()
	long := w
	if h > long {
		long = h
	}
	if long <= maxEdge {
		return encodeImage(src)
	}
	scale := float64(maxEdge) / float64(long)
	nw := int(float64(w) * scale)
	nh := int(float64(h) * scale)
	if nw < 1 {
		nw = 1
	}
	if nh < 1 {
		nh = 1
	}
	dst := image.NewRGBA(image.Rect(0, 0, nw, nh))
	// Nearest-neighbor keeps normalization bounded.
	for y := 0; y < nh; y++ {
		sy := b.Min.Y + int(float64(y)/scale)
		for x := 0; x < nw; x++ {
			sx := b.Min.X + int(float64(x)/scale)
			dst.Set(x, y, src.At(sx, sy))
		}
	}
	return encodeImage(dst)
}

// encodeImage serializes a downscaled frame as PNG.
func encodeImage(img image.Image) ([]byte, string, error) {
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		return nil, "", err
	}
	return buf.Bytes(), "image/png", nil
}

// PerceiveImageFixtureDigest returns a stable digest for mock/recording fixtures.
func PerceiveImageFixtureDigest(raw []byte) string {
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:8])
}

// VisualBytesResolver loads stored artifact bytes for perceive wire encoding.
// Missing or deleted bytes return !ok.
type VisualBytesResolver func(sessionID, artifactID string) (bytes []byte, mime string, ok bool)

var resolveVisualBytes VisualBytesResolver

// SetVisualBytesResolver wires store-backed perceive resolution for tool visuals.
func SetVisualBytesResolver(fn VisualBytesResolver) {
	resolveVisualBytes = fn
}

// wireImageBound caps artifacts resolved for provider wire encoding.
var wireImageBound atomic.Int64

// SetWireImageBound installs that ceiling at boot.
func SetWireImageBound(max bytebound.Transport) { wireImageBound.Store(max.Int64()) }

func wireImageCeiling() bytebound.Transport { return bytebound.Transport(wireImageBound.Load()) }

func HasVisualResolver() bool { return resolveVisualBytes != nil }
