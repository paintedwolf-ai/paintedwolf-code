package visualscreen

import (
	"bytes"
	"image"
	"image/color"
	"image/draw"
	"image/png"
	"sort"

	"github.com/lycaon/lycaon/internal/secretmatch"
)

// overlapping returns the segments a match's rune range touches.
func overlapping(segments []segment, m secretmatch.Match) []segment {
	var out []segment
	for _, seg := range segments {
		if m.Start < seg.end && m.End > seg.start {
			out = append(out, seg)
		}
	}
	return out
}

type byteRange struct{ start, end int }

// redactSVG replaces each matched run in the markup it was read from. Every
// part of every match must come from a verbatim markup range.
func redactSVG(raw []byte, scanned *ScannedVisual, matches []secretmatch.Match) ([]byte, bool) {
	runes := []rune(scanned.ExtractedText)
	var ranges []byteRange
	for _, m := range matches {
		segs := overlapping(scanned.segments, m)
		if len(segs) == 0 {
			return nil, false
		}
		for _, seg := range segs {
			if seg.kind != segmentMarkup || seg.rawStart < 0 {
				return nil, false
			}
			from, to := max(m.Start, seg.start), min(m.End, seg.end)
			prefix := len(string(runes[seg.start:from]))
			length := len(string(runes[from:to]))
			ranges = append(ranges, byteRange{seg.rawStart + prefix, seg.rawStart + prefix + length})
		}
	}
	ranges = mergeRanges(ranges)
	out := append([]byte(nil), raw...)
	for i := len(ranges) - 1; i >= 0; i-- {
		r := ranges[i]
		if r.start < 0 || r.end > len(out) || r.start >= r.end {
			return nil, false
		}
		out = append(out[:r.start], append([]byte("[REDACTED]"), out[r.end:]...)...)
	}
	return out, true
}

func mergeRanges(in []byteRange) []byteRange {
	sort.Slice(in, func(i, j int) bool { return in[i].start < in[j].start })
	var out []byteRange
	for _, r := range in {
		if n := len(out); n > 0 && r.start <= out[n-1].end {
			out[n-1].end = max(out[n-1].end, r.end)
			continue
		}
		out = append(out, r)
	}
	return out
}

// maskRaster blacks out the recognized spans that produced each match and
// re-encodes the raster, which also drops container metadata. A match that
// maps to no recognized span masks every span.
func maskRaster(raw []byte, scanned *ScannedVisual, matches []secretmatch.Match) ([]byte, bool) {
	masked := make(map[int]struct{})
	maskAll := false
	for _, m := range matches {
		segs := overlapping(scanned.segments, m)
		mapped := false
		for _, seg := range segs {
			switch seg.kind {
			case segmentOCR:
				masked[seg.span] = struct{}{}
				mapped = true
			case segmentMetadata:
				mapped = true
			default:
				return nil, false
			}
		}
		if !mapped {
			maskAll = true
		}
	}
	if maskAll {
		if len(scanned.Spans) == 0 {
			return nil, false
		}
		for i := range scanned.Spans {
			masked[i] = struct{}{}
		}
	}

	img, err := decodeRaster(raw)
	if err != nil {
		return nil, false
	}
	bounds := img.Bounds()
	dst := image.NewRGBA(bounds)
	draw.Draw(dst, bounds, img, bounds.Min, draw.Src)
	black := &image.Uniform{C: color.RGBA{A: 255}}
	for i := range masked {
		box := scanned.Spans[i].BoundingBox
		x0 := bounds.Min.X + int(box[0]*float64(bounds.Dx()))
		y0 := bounds.Min.Y + int(box[1]*float64(bounds.Dy()))
		w := int(box[2]*float64(bounds.Dx()) + 0.999)
		h := int(box[3]*float64(bounds.Dy()) + 0.999)
		rect := image.Rect(x0, y0, x0+w, y0+h).Intersect(bounds)
		if rect.Empty() {
			return nil, false
		}
		draw.Draw(dst, rect, black, image.Point{}, draw.Src)
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, dst); err != nil {
		return nil, false
	}
	return buf.Bytes(), true
}
