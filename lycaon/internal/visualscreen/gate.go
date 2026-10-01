package visualscreen

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"image/png"
	"sort"

	"github.com/lycaon/lycaon/internal/gate"
	"github.com/lycaon/lycaon/internal/secretmatch"
)

// ErrVisualSecretWithheld is returned when perception of an image is declined
// or its review could not reach a person.
var ErrVisualSecretWithheld = errors.New("visual perception withheld")

// Perception is what the model receives after screening.
type Perception string

const (
	// PerceptionOriginal sends the image as supplied.
	PerceptionOriginal Perception = "original"
	// PerceptionRedacted sends an image whose matched text was removed.
	PerceptionRedacted Perception = "redacted"
	// PerceptionWithheld sends no pixels: a redacted send was chosen but a
	// match could not be located in the image.
	PerceptionWithheld Perception = "withheld_unredactable"
)

// Gate screens visual assets for credentials before model perception.
type Gate struct {
	scanner *Scanner
	matcher *secretmatch.Matcher
	ask     secretmatch.AskFunc
}

// NewGate creates a visual screening gate.
func NewGate(scanner *Scanner, matcher *secretmatch.Matcher, ask secretmatch.AskFunc) *Gate {
	if scanner == nil {
		scanner = NewScanner(nil)
	}
	return &Gate{scanner: scanner, matcher: matcher, ask: ask}
}

// VisualScreenInput holds contextual facts for visual screening.
type VisualScreenInput struct {
	SessionID     string
	ProjectID     string
	ToolName      string
	ToolCallID    string
	Mime          string
	RawBytes      []byte
	SourcePath    string
	PublicInbound bool
}

// VisualScreenOutcome holds the screened text and what perception receives.
type VisualScreenOutcome struct {
	ExtractedText string
	PerceiveBytes []byte
	Mime          string
	Perception    Perception
	// Gap names text the screen could not read.
	Gap secretmatch.ScreeningGap
}

// PermitPerception reports whether any pixels may reach the model.
func (o *VisualScreenOutcome) PermitPerception() bool {
	return o != nil && o.Perception != PerceptionWithheld
}

// Screen reads the visual's text, raises the secret card for a match or for
// text it could not read, and applies the person's decision.
func (g *Gate) Screen(ctx context.Context, in VisualScreenInput) (*VisualScreenOutcome, error) {
	scanned, err := g.scan(ctx, in)
	if err != nil {
		return nil, err
	}
	outcome := &VisualScreenOutcome{
		ExtractedText: scanned.ExtractedText,
		PerceiveBytes: in.RawBytes,
		Mime:          in.Mime,
		Perception:    PerceptionOriginal,
		Gap:           scanned.Gap,
	}
	if g.matcher == nil || g.matcher.Inert() {
		return outcome, nil
	}
	var matches []secretmatch.Match
	if scanned.ExtractedText != "" {
		matches = g.matcher.ScreenContext(ctx, scanned.ExtractedText)
	}
	if len(matches) == 0 && scanned.Gap == "" {
		return outcome, nil
	}
	if g.ask == nil {
		return nil, ErrVisualSecretWithheld
	}
	resolution, err := g.ask(ctx, visualAlert(in, matches, scanned.Gap))
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrVisualSecretWithheld, err)
	}
	if resolution.Decision.Blocks() {
		return nil, ErrVisualSecretWithheld
	}
	if resolution.Decision == secretmatch.SendRedacted {
		g.redact(ctx, in, scanned, matches, outcome)
	}
	return outcome, nil
}

func (g *Gate) scan(ctx context.Context, in VisualScreenInput) (*ScannedVisual, error) {
	scanner := g.scanner
	if scanner == nil {
		scanner = NewScanner(nil)
	}
	return scanner.Scan(ctx, in.Mime, in.RawBytes)
}

func visualAlert(in VisualScreenInput, matches []secretmatch.Match, gap secretmatch.ScreeningGap) secretmatch.Alert {
	source := gate.SecretSourceUnknown
	if in.PublicInbound {
		source = gate.SecretSourcePublicInbound
	}
	origin := secretmatch.OriginField
	if in.SourcePath != "" {
		origin = secretmatch.OriginFile
	}
	alert := secretmatch.Alert{
		SessionID:        in.SessionID,
		ProjectID:        in.ProjectID,
		ToolCallID:       in.ToolCallID,
		Surface:          secretmatch.SurfaceVisualModel,
		DestinationID:    string(secretmatch.DestinationModelProvider),
		DestinationLabel: "model provider",
		Occurrences:      len(matches),
		SourceKind:       secretmatch.SourceVisualCapture,
		SourceTool:       in.ToolName,
		SourcePath:       in.SourcePath,
		OriginKind:       origin,
		Source:           source,
		Fingerprints:     secretmatch.Fingerprints(matches),
		ScreeningGap:     gap,
	}
	if len(matches) > 0 {
		alert.RuleID = matches[0].RuleID
		alert.RuleTitle = matches[0].Title
		alert.GenericShape = matches[0].GenericShape
	} else {
		alert.RuleID = secretmatch.UnscreenedRuleID
		alert.RuleTitle = gap.Label()
	}
	if gap != "" {
		alert.Fingerprints = append(alert.Fingerprints, secretmatch.UnscreenedFingerprint)
	}
	return alert
}

// redact removes every match from the perceived bytes, or withholds the pixels
// when any match cannot be located in them.
func (g *Gate) redact(ctx context.Context, in VisualScreenInput, scanned *ScannedVisual, matches []secretmatch.Match, outcome *VisualScreenOutcome) {
	outcome.ExtractedText = g.matcher.RedactString(ctx, scanned.ExtractedText)
	withhold := func() {
		outcome.Perception = PerceptionWithheld
		outcome.PerceiveBytes = nil
	}
	if scanned.Gap != "" {
		withhold()
		return
	}
	switch scanned.Kind {
	case KindSVG:
		redacted, ok := redactSVG(in.RawBytes, scanned, matches)
		if !ok || g.svgStillMatches(ctx, redacted) {
			withhold()
			return
		}
		outcome.PerceiveBytes = redacted
	case KindRaster:
		masked, ok := maskRaster(in.RawBytes, scanned, matches)
		if !ok {
			withhold()
			return
		}
		outcome.PerceiveBytes = masked
		outcome.Mime = "image/png"
	default:
		withhold()
		return
	}
	outcome.Perception = PerceptionRedacted
}

func (g *Gate) svgStillMatches(ctx context.Context, svg []byte) bool {
	b := &textBuilder{}
	extractSVGText(svg, b)
	return len(g.matcher.ScreenContext(ctx, b.text.String())) > 0
}

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

	img, _, err := image.Decode(bytes.NewReader(raw))
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
