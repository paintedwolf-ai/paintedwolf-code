// Package captureprojection produces secret-screened capture views.
package captureprojection

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"image/jpeg"
	"image/png"
	"io"
	"math"
	"sort"
	"strings"
	"sync"
	"unicode"
	"unicode/utf8"

	"github.com/lycaon/lycaon/internal/secretmatch"
)

// ErrUnavailable means capture screening is not configured.
var ErrUnavailable = errors.New("capture screening unavailable")

// ManagedSecretPrimer admits the current project's protected values to exact
// matching before its first capture projection.
type ManagedSecretPrimer func(context.Context, string, string) error

// ManagedSecretGeneration identifies the current protected-value set.
type ManagedSecretGeneration func() uint64

// Scope identifies the capture without containing any captured bytes.
type Scope struct {
	ProjectID, RootSessionID, SessionID string
}

// ScopeFor binds a capture to the task tree used for secret matching.
func ScopeFor(projectID, parentSessionID, sessionID string) Scope {
	root := strings.TrimSpace(parentSessionID)
	if root == "" {
		root = strings.TrimSpace(sessionID)
	}
	return Scope{
		ProjectID:     strings.TrimSpace(projectID),
		RootSessionID: root,
		SessionID:     strings.TrimSpace(sessionID),
	}
}

// Span is one masked range of a projection's input, in byte offsets.
type Span struct{ Start, End int }

// Metadata is safe projection provenance.
type Metadata struct {
	RedactedCount      int
	CatalogVersion     string
	StructuredCoverage bool
}

// Text is a screened text projection.
type Text struct {
	Value    string
	Metadata Metadata
}

// TextSpan binds source runes to mask rectangles.
type TextSpan struct {
	Start int    `json:"start"`
	End   int    `json:"end"`
	Rects []Rect `json:"rects"`
}

// Region binds page text to per-rune geometry.
type Region struct {
	Text  string     `json:"text"`
	Spans []TextSpan `json:"spans"`
}

// Rect is a CSS-pixel mask rectangle.
type Rect struct{ X, Y, Width, Height float64 }

// Projector holds shared matching and priming state.
type Projector struct {
	matcher    *secretmatch.Matcher
	primer     ManagedSecretPrimer
	generation ManagedSecretGeneration
	primed     sync.Map
}

func New(matcher *secretmatch.Matcher, primer ManagedSecretPrimer) *Projector {
	return &Projector{matcher: matcher, primer: primer}
}

// SetManagedSecretGeneration configures primer invalidation.
func (p *Projector) SetManagedSecretGeneration(generation ManagedSecretGeneration) {
	if p != nil {
		p.generation = generation
	}
}

func (p *Projector) prepare(ctx context.Context, scope Scope) error {
	if p == nil || p.primer == nil {
		return nil
	}
	projectID := strings.TrimSpace(scope.ProjectID)
	rootID := strings.TrimSpace(scope.RootSessionID)
	if projectID == "" || rootID == "" {
		return nil
	}
	key := projectID + "\x00" + rootID
	generation := uint64(1)
	if p.generation != nil {
		generation = p.generation()
	}
	if primed, ok := p.primed.Load(key); ok && primed == generation {
		return nil
	}
	if err := p.primer(ctx, projectID, rootID); err != nil {
		return err
	}
	p.primed.Store(key, generation)
	return nil
}

func scopeContext(ctx context.Context, scope Scope) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	return secretmatch.WithAskAttribution(ctx, secretmatch.AskAttribution{
		ProjectID:     strings.TrimSpace(scope.ProjectID),
		RootSessionID: strings.TrimSpace(scope.RootSessionID),
		SessionID:     strings.TrimSpace(scope.SessionID),
	})
}

// Text projects captured text with the same deterministic markers used by
// transcript and outbound screening.
func (p *Projector) Text(ctx context.Context, scope Scope, label, value string) (Text, error) {
	projected, _, err := p.TextSpans(ctx, scope, label, value)
	return projected, err
}

// TextSpans projects text and reports masked input byte ranges.
func (p *Projector) TextSpans(
	ctx context.Context, scope Scope, label, value string,
) (Text, []Span, error) {
	if p == nil || p.matcher == nil {
		return Text{}, nil, ErrUnavailable
	}
	ctx = scopeContext(ctx, scope)
	if err := p.prepare(ctx, scope); err != nil {
		return Text{}, nil, err
	}
	var matched []secretmatch.Match
	safe, spans := p.matcher.RedactLabeledSpansWhere(ctx, label, value, func(hit secretmatch.Match) bool {
		matched = append(matched, hit)
		return true
	})
	return Text{Value: safe, Metadata: Metadata{
		RedactedCount: len(spans), CatalogVersion: p.matcher.CatalogVersion(), StructuredCoverage: true,
	}}, byteSpans(value, matched), nil
}

// byteSpans converts rune-offset matches to input byte offsets in one pass.
func byteSpans(value string, hits []secretmatch.Match) []Span {
	if len(hits) == 0 {
		return nil
	}
	bounds := make([]int, 0, 2*len(hits))
	for _, hit := range hits {
		bounds = append(bounds, hit.Start, hit.End)
	}
	sort.Ints(bounds)
	at := make(map[int]int, len(bounds))
	next, runeIdx := 0, 0
	for byteIdx := range value {
		for next < len(bounds) && bounds[next] == runeIdx {
			at[bounds[next]] = byteIdx
			next++
		}
		runeIdx++
	}
	for next < len(bounds) {
		at[bounds[next]] = len(value)
		next++
	}
	out := make([]Span, 0, len(hits))
	for _, hit := range hits {
		start, end := at[hit.Start], at[hit.End]
		if end > start {
			out = append(out, Span{Start: start, End: end})
		}
	}
	return out
}

// JSON screens strings and object keys while preserving JSON structure.
func (p *Projector) JSON(ctx context.Context, scope Scope, label string, raw []byte) ([]byte, Metadata, error) {
	if p == nil || p.matcher == nil {
		return nil, Metadata{}, ErrUnavailable
	}
	if len(raw) == 0 {
		return nil, Metadata{StructuredCoverage: true}, nil
	}
	var value any
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if err := decoder.Decode(&value); err != nil {
		return nil, Metadata{}, fmt.Errorf("decode captured JSON: %w", err)
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		if err == nil {
			return nil, Metadata{}, fmt.Errorf("decode captured JSON: multiple values")
		}
		return nil, Metadata{}, fmt.Errorf("decode captured JSON trailer: %w", err)
	}
	meta := Metadata{StructuredCoverage: true}
	projected, err := p.projectJSONValue(ctx, scope, label, value, &meta)
	if err != nil {
		return nil, Metadata{}, err
	}
	out, err := json.Marshal(projected)
	if err != nil {
		return nil, Metadata{}, fmt.Errorf("encode screened capture JSON: %w", err)
	}
	return out, meta, nil
}

func (p *Projector) projectJSONValue(
	ctx context.Context, scope Scope, label string, value any, meta *Metadata,
) (any, error) {
	switch typed := value.(type) {
	case string:
		projected, err := p.Text(ctx, scope, label, typed)
		if err != nil {
			return nil, err
		}
		mergeMetadata(meta, projected.Metadata)
		return projected.Value, nil
	case []any:
		out := make([]any, len(typed))
		for i := range typed {
			projected, err := p.projectJSONValue(ctx, scope, label, typed[i], meta)
			if err != nil {
				return nil, err
			}
			out[i] = projected
		}
		return out, nil
	case map[string]any:
		out := make(map[string]any, len(typed))
		keys := make([]string, 0, len(typed))
		for key := range typed {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		for _, key := range keys {
			projectedKey, err := p.Text(ctx, scope, label+".key", key)
			if err != nil {
				return nil, err
			}
			mergeMetadata(meta, projectedKey.Metadata)
			safeKey := uniqueJSONKey(out, projectedKey.Value)
			projected, err := p.projectJSONValue(ctx, scope, label+"."+safeKey, typed[key], meta)
			if err != nil {
				return nil, err
			}
			out[safeKey] = projected
		}
		return out, nil
	default:
		return value, nil
	}
}

func uniqueJSONKey(values map[string]any, key string) string {
	if _, exists := values[key]; !exists {
		return key
	}
	for i := 2; ; i++ {
		candidate := fmt.Sprintf("%s#%d", key, i)
		if _, exists := values[candidate]; !exists {
			return candidate
		}
	}
}

func mergeMetadata(dst *Metadata, src Metadata) {
	if dst == nil {
		return
	}
	dst.RedactedCount += src.RedactedCount
	if src.CatalogVersion != "" {
		dst.CatalogVersion = src.CatalogVersion
	}
	dst.StructuredCoverage = dst.StructuredCoverage && src.StructuredCoverage
}

// Grid screens physical rows and their fixed-width concatenation.
func (p *Projector) Grid(ctx context.Context, scope Scope, label string, lines []string) ([]string, Metadata, error) {
	if p == nil || p.matcher == nil {
		return nil, Metadata{}, ErrUnavailable
	}
	ctx = scopeContext(ctx, scope)
	if err := p.prepare(ctx, scope); err != nil {
		return nil, Metadata{}, err
	}
	grid := newRuneGrid(lines)
	grid.maskPhysical(p.matcher.ScreenLabeledContext(ctx, label, grid.physicalText()))
	if logical, width := grid.logicalText(); width > 0 {
		grid.maskLogical(p.matcher.ScreenLabeledContext(ctx, label, logical), width)
	}
	return grid.lines(), Metadata{
		RedactedCount: grid.maskedRuns(), CatalogVersion: p.matcher.CatalogVersion(), StructuredCoverage: true,
	}, nil
}

// maskedRune replaces a screened cell without moving its neighbours.
const maskedRune = '•'

// runeGrid holds one terminal screen as fixed cells plus a per-cell mask.
type runeGrid struct {
	rows   [][]rune
	masked [][]bool
	width  int
}

func newRuneGrid(lines []string) *runeGrid {
	g := &runeGrid{rows: make([][]rune, len(lines)), masked: make([][]bool, len(lines))}
	for i, line := range lines {
		g.rows[i] = []rune(line)
		g.masked[i] = make([]bool, len(g.rows[i]))
		if len(g.rows[i]) > g.width {
			g.width = len(g.rows[i])
		}
	}
	return g
}

func (g *runeGrid) physicalText() string {
	parts := make([]string, len(g.rows))
	for i := range g.rows {
		parts[i] = string(g.rows[i])
	}
	return strings.Join(parts, "\n")
}

func (g *runeGrid) logicalText() (string, int) {
	if g.width == 0 {
		return "", 0
	}
	var b strings.Builder
	for _, row := range g.rows {
		b.WriteString(string(row))
		b.WriteString(strings.Repeat(" ", g.width-len(row)))
	}
	return b.String(), g.width
}

func (g *runeGrid) maskPhysical(hits []secretmatch.Match) {
	starts := make([]int, len(g.rows))
	offset := 0
	for i, row := range g.rows {
		starts[i] = offset
		offset += len(row) + 1
	}
	for _, hit := range hits {
		for i, row := range g.rows {
			if starts[i] >= hit.End {
				break
			}
			if starts[i]+len(row) <= hit.Start {
				continue
			}
			for col := max(hit.Start-starts[i], 0); col < min(hit.End-starts[i], len(row)); col++ {
				g.masked[i][col] = true
			}
		}
	}
}

func (g *runeGrid) maskLogical(hits []secretmatch.Match, width int) {
	for _, hit := range hits {
		start, end := max(hit.Start, 0), min(hit.End, width*len(g.rows))
		for at := start; at < end; at++ {
			row, col := at/width, at%width
			if col < len(g.masked[row]) {
				g.masked[row][col] = true
			}
		}
	}
}

// maskedRuns counts contiguous masked cells per row.
func (g *runeGrid) maskedRuns() int {
	runs := 0
	for _, row := range g.masked {
		prev := false
		for _, on := range row {
			if on && !prev {
				runs++
			}
			prev = on
		}
	}
	return runs
}

func (g *runeGrid) lines() []string {
	out := make([]string, len(g.rows))
	for i, row := range g.rows {
		cells := make([]rune, len(row))
		for col, r := range row {
			if g.masked[i][col] {
				cells[col] = maskedRune
				continue
			}
			cells[col] = r
		}
		out[i] = string(cells)
	}
	return out
}

// screenRegions checks adjacent and line-separated region text.
func (p *Projector) screenRegions(ctx context.Context, regions []Region) (int, []Rect) {
	if len(regions) == 0 {
		return 0, nil
	}
	count := 0
	seenRects := make(map[Rect]struct{})
	var rects []Rect
	for _, separator := range []string{"", "\n"} {
		var body strings.Builder
		starts := make([]int, len(regions))
		ends := make([]int, len(regions))
		at := 0
		for i, region := range regions {
			if i > 0 {
				body.WriteString(separator)
				at += utf8.RuneCountInString(separator)
			}
			starts[i] = at
			at += utf8.RuneCountInString(region.Text)
			ends[i] = at
			body.WriteString(region.Text)
		}
		hits := p.matcher.ScreenLabeledContext(ctx, "capture.browser", body.String())
		coveredHits := 0
		for _, hit := range hits {
			hasGeometry := false
			for i := range regions {
				localStart := max(hit.Start-starts[i], 0)
				localEnd := min(hit.End-starts[i], ends[i]-starts[i])
				if localEnd <= localStart {
					continue
				}
				for _, span := range regions[i].Spans {
					if span.End <= localStart || span.Start >= localEnd {
						continue
					}
					hasGeometry = hasGeometry || len(span.Rects) > 0
					for _, rect := range span.Rects {
						if _, exists := seenRects[rect]; exists {
							continue
						}
						seenRects[rect] = struct{}{}
						rects = append(rects, rect)
					}
				}
			}
			if hasGeometry {
				coveredHits++
			}
		}
		count = max(count, coveredHits)
	}
	return count, rects
}

// RasterMask is the screened text geometry of one page sample, in CSS pixels. Frames
// painted near the sample reuse it without screening the text again.
type RasterMask struct {
	rects     []Rect
	cssWidth  float64
	cssHeight float64
	Metadata  Metadata
}

// Empty reports whether the mask hides nothing.
func (m RasterMask) Empty() bool {
	return len(m.rects) == 0
}

// Union masks everything either mask hides. Coverage is structured only if both are.
func (m RasterMask) Union(o RasterMask) RasterMask {
	out := RasterMask{
		rects: append(append([]Rect(nil), m.rects...), o.rects...), cssWidth: max(m.cssWidth, o.cssWidth), cssHeight: max(m.cssHeight, o.cssHeight),
		Metadata: Metadata{
			RedactedCount:  max(m.Metadata.RedactedCount, o.Metadata.RedactedCount),
			CatalogVersion: m.Metadata.CatalogVersion,
			StructuredCoverage: m.Metadata.StructuredCoverage && o.Metadata.StructuredCoverage,
		},
	}
	if out.Metadata.CatalogVersion == "" {
		out.Metadata.CatalogVersion = o.Metadata.CatalogVersion
	}
	return out
}

// ScreenRegions screens page text once and returns the geometry to mask in rasters of it.
func (p *Projector) ScreenRegions(
	ctx context.Context,
	scope Scope,
	cssWidth, cssHeight float64,
	regions []Region,
	structuredCoverage bool,
) (RasterMask, error) {
	if p == nil || p.matcher == nil {
		return RasterMask{}, ErrUnavailable
	}
	ctx = scopeContext(ctx, scope)
	if err := p.prepare(ctx, scope); err != nil {
		return RasterMask{}, err
	}
	text, exactGeometry := exactRuneRegions(regions)
	hitCount, hitRects := p.screenRegions(ctx, text)
	return RasterMask{
		rects: hitRects, cssWidth: cssWidth, cssHeight: cssHeight,
		Metadata: Metadata{
			RedactedCount:  hitCount,
			CatalogVersion: p.matcher.CatalogVersion(), StructuredCoverage: structuredCoverage && exactGeometry,
		},
	}, nil
}

// Apply paints the mask over a raster. A raster the mask does not touch is returned as is.
func (m RasterMask) Apply(mime string, raw []byte) ([]byte, error) {
	if m.Empty() {
		return append([]byte(nil), raw...), nil
	}
	decoded, format, err := image.Decode(bytes.NewReader(raw))
	if err != nil {
		return nil, fmt.Errorf("decode captured raster: %w", err)
	}
	bounds := decoded.Bounds()
	canvas := image.NewRGBA(bounds)
	draw.Draw(canvas, bounds, decoded, bounds.Min, draw.Src)
	cssWidth, cssHeight := m.cssWidth, m.cssHeight
	if cssWidth <= 0 {
		cssWidth = float64(bounds.Dx())
	}
	if cssHeight <= 0 {
		cssHeight = float64(bounds.Dy())
	}
	sx, sy := float64(bounds.Dx())/cssWidth, float64(bounds.Dy())/cssHeight
	for _, mask := range m.rects {
		r := scaledMaskRect(bounds, mask, sx, sy)
		if !r.Empty() {
			draw.Draw(canvas, r, &image.Uniform{C: color.RGBA{R: 20, G: 24, B: 31, A: 255}}, image.Point{}, draw.Src)
		}
	}
	var out bytes.Buffer
	switch strings.ToLower(strings.TrimSpace(mime)) {
	case "image/jpeg", "image/jpg":
		err = jpeg.Encode(&out, canvas, &jpeg.Options{Quality: 45})
	case "image/png":
		err = png.Encode(&out, canvas)
	default:
		if format == "jpeg" {
			err = jpeg.Encode(&out, canvas, &jpeg.Options{Quality: 45})
		} else {
			err = png.Encode(&out, canvas)
		}
	}
	if err != nil {
		return nil, fmt.Errorf("encode screened raster: %w", err)
	}
	return out.Bytes(), nil
}

// Raster masks regions overlapping screened text. All other capture pixels remain unchanged.
func (p *Projector) Raster(
	ctx context.Context,
	scope Scope,
	mime string,
	raw []byte,
	cssWidth, cssHeight float64,
	regions []Region,
	structuredCoverage bool,
) ([]byte, Metadata, error) {
	mask, err := p.ScreenRegions(ctx, scope, cssWidth, cssHeight, regions, structuredCoverage)
	if err != nil {
		return nil, Metadata{}, err
	}
	out, err := mask.Apply(mime, raw)
	if err != nil {
		return nil, Metadata{}, err
	}
	return out, mask.Metadata, nil
}

// exactRuneRegions rejects spans that could mask unrelated pixels.
func exactRuneRegions(regions []Region) ([]Region, bool) {
	exact := true
	out := make([]Region, 0, len(regions))
	for _, region := range regions {
		if region.Text == "" {
			continue
		}
		if len(region.Spans) == 0 {
			exact = false
			continue
		}
		runeCount := utf8.RuneCountInString(region.Text)
		clean := Region{Text: region.Text, Spans: make([]TextSpan, 0, len(region.Spans))}
		covered := make([]bool, runeCount)
		for _, span := range region.Spans {
			if span.Start < 0 || span.End != span.Start+1 || span.End > runeCount {
				exact = false
				continue
			}
			valid := make([]Rect, 0, len(span.Rects))
			for _, rect := range span.Rects {
				if !validMaskRect(rect) {
					exact = false
					continue
				}
				valid = append(valid, rect)
			}
			if len(valid) == 0 {
				continue
			}
			span.Rects = valid
			covered[span.Start] = true
			clean.Spans = append(clean.Spans, span)
		}
		for index, r := range []rune(region.Text) {
			if !unicode.IsSpace(r) && !covered[index] {
				exact = false
			}
		}
		if len(clean.Spans) > 0 {
			out = append(out, clean)
		}
	}
	return out, exact
}

func validMaskRect(rect Rect) bool {
	return rect.Width > 0 && rect.Height > 0 &&
		!math.IsNaN(rect.X) && !math.IsNaN(rect.Y) &&
		!math.IsNaN(rect.Width) && !math.IsNaN(rect.Height) &&
		!math.IsInf(rect.X, 0) && !math.IsInf(rect.Y, 0) &&
		!math.IsInf(rect.Width, 0) && !math.IsInf(rect.Height, 0)
}

func scaledMaskRect(bounds image.Rectangle, mask Rect, sx, sy float64) image.Rectangle {
	return image.Rect(
		bounds.Min.X+int(mask.X*sx), bounds.Min.Y+int(mask.Y*sy),
		bounds.Min.X+int((mask.X+mask.Width)*sx+0.999), bounds.Min.Y+int((mask.Y+mask.Height)*sy+0.999),
	).Intersect(bounds)
}
