package visualscreen

import (
	"context"
	"errors"
	"fmt"
	"strings"

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
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	scanned, err := g.scan(ctx, in)
	if err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
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
	matches := g.screenVisual(ctx, scanned)
	if err := ctx.Err(); err != nil {
		return nil, err
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
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if resolution.Decision.Blocks() {
		return nil, ErrVisualSecretWithheld
	}
	if resolution.Decision == secretmatch.SendRedacted {
		g.redact(ctx, in, scanned, matches, outcome)
	}
	if err := ctx.Err(); err != nil {
		return nil, err
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

func (g *Gate) screenVisual(ctx context.Context, scanned *ScannedVisual) []secretmatch.Match {
	if scanned == nil {
		return nil
	}
	var matches []secretmatch.Match
	if scanned.ExtractedText != "" {
		matches = g.matcher.ScreenContext(ctx, scanned.ExtractedText)
	}
	runes := []rune(scanned.ExtractedText)
	for _, seg := range scanned.segments {
		if !seg.ocr && seg.kind != segmentOCR {
			continue
		}
		var segHits []secretmatch.Match
		segLen := seg.end - seg.start
		if seg.start >= 0 && segLen > 0 && seg.end <= len(runes) {
			segText := string(runes[seg.start:seg.end])
			for _, m := range g.matcher.ScreenOCRContext(ctx, segText) {
				m.Start += seg.start
				m.End += seg.start
				segHits = append(segHits, m)
			}
		}
		for _, cand := range seg.candidates {
			trimmed := strings.TrimSpace(cand)
			if trimmed == "" {
				continue
			}
			candRunes := len([]rune(trimmed))
			for _, m := range g.matcher.ScreenOCRContext(ctx, trimmed) {
				if segLen > 0 && candRunes == segLen && m.Start >= 0 && m.End <= segLen && m.End > m.Start {
					m.Start += seg.start
					m.End += seg.start
				} else if segLen > 0 {
					m.Start = seg.start
					m.End = seg.end
				}
				if !overlapsAnyMatch(segHits, m) {
					segHits = append(segHits, m)
				}
			}
		}
		matches = append(matches, segHits...)
	}
	return secretmatch.DedupeAndSort(matches)
}

func overlapsAnyMatch(hits []secretmatch.Match, m secretmatch.Match) bool {
	for _, h := range hits {
		if h.Start < m.End && m.Start < h.End {
			return true
		}
	}
	return false
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
	outcome.ExtractedText = secretmatch.RedactMatches(scanned.ExtractedText, matches)
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
