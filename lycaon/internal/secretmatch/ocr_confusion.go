package secretmatch

import (
	"context"
	"strings"
	"sync"
	"unicode/utf8"

	gconfig "github.com/zricethezav/gitleaks/v8/config"
	"github.com/zricethezav/gitleaks/v8/detect"
	"github.com/zricethezav/gitleaks/v8/report"
)

type ocrState struct {
	baseConfig gconfig.Config
	confusions ocrConfusionCatalog
	once       sync.Once
	detector   *detect.Detector
	cache      *screenCache
}

func newOCRState(cfg gconfig.Config, confusions ocrConfusionCatalog) *ocrState {
	return &ocrState{
		baseConfig: cfg,
		confusions: confusions,
		cache:      newScreenCache(),
	}
}

func (s *ocrState) detectorInstance() *detect.Detector {
	if s == nil {
		return nil
	}
	s.once.Do(func() {
		s.detector = buildOCRDetector(s.baseConfig, s.confusions)
	})
	return s.detector
}

// ScreenOCR finds secret matches while tolerating cataloged OCR glyph confusions.
func (m *Matcher) ScreenOCR(s string) []Match {
	return m.ScreenOCRContext(context.Background(), s)
}

// ScreenOCRContext returns ranked, non-overlapping findings under OCR glyph confusion tolerance.
func (m *Matcher) ScreenOCRContext(ctx context.Context, s string) []Match {
	return dedupeAndSort(screenOCRRaw(ctx, m, s))
}

// RedactOCRString masks spans detected under OCR glyph confusion tolerance.
func (m *Matcher) RedactOCRString(ctx context.Context, s string) string {
	out, _ := redactMatches(s, screenOCRRaw(ctx, m, s))
	return out
}

func screenOCRRaw(ctx context.Context, m *Matcher, s string) []Match {
	if m == nil || ctx == nil || m.ocr == nil {
		return m.screenRaw(ctx, s)
	}
	mask := maskReferenceTokens(s)
	hits := catalogMatchesOCR(ctx, m, mask.text)
	hits = append(hits, harvestMatchesOCR(ctx, m, mask.text)...)
	hits = mask.outside(hits)
	if err := propagateNonDisclosable(hits, ctx.Err); err != nil {
		return nil
	}
	hits, err := mergeContainerEvidence(hits, ctx.Err)
	if err != nil {
		return nil
	}
	return m.dropIgnored(ctx, hits)
}

func catalogMatchesOCR(ctx context.Context, m *Matcher, s string) []Match {
	if m == nil || m.inert || m.ocr == nil {
		return nil
	}
	d := m.ocr.detectorInstance()
	if d == nil {
		return nil
	}
	if s == "" {
		return runCatalogOCR(ctx, m, d, s)
	}
	key := screenCacheKeyFor(s)
	if hits, ok := m.ocr.cache.get(key); ok {
		return hits
	}
	hits := runCatalogOCR(ctx, m, d, s)
	if ctx.Err() == nil {
		m.ocr.cache.put(key, hits)
	}
	return hits
}

func runCatalogOCR(ctx context.Context, m *Matcher, d *detect.Detector, s string) []Match {
	if len(s) <= m.screenWindowBytes() {
		return screenWindowOCR(ctx, m, d, s, 0)
	}
	var hits []Match
	for _, w := range screenWindows(s, m.screenWindowBytes(), m.screenOverlap()) {
		if ctx.Err() != nil {
			break
		}
		hits = append(hits, screenWindowOCR(ctx, m, d, s[w.start:w.end], w.runeOffset)...)
	}
	return hits
}

func screenWindowOCR(ctx context.Context, m *Matcher, d *detect.Detector, window string, runeOffset int) []Match {
	hits := detectWindowOCR(ctx, m, d, window, window, runeOffset)
	if normWindow, changed := normalizeBoundarySlashes(window); changed {
		hits = append(hits, detectWindowOCR(ctx, m, d, window, normWindow, runeOffset)...)
	}
	return hits
}

func detectWindowOCR(ctx context.Context, m *Matcher, d *detect.Detector, rawWindow, probeWindow string, runeOffset int) []Match {
	//nolint:staticcheck // The detector does not expose a replacement fragment.
	findings := d.DetectContext(ctx, detect.Fragment{Raw: probeWindow})
	if len(findings) == 0 {
		return nil
	}
	lineIdx := buildLineIndex(probeWindow)
	rawRunes := []rune(rawWindow)
	hits := make([]Match, 0, len(findings))
	for i := range findings {
		if match, ok := findingToMatchOCR(rawRunes, probeWindow, &findings[i], lineIdx, m); ok {
			match.Start += runeOffset
			match.End += runeOffset
			hits = append(hits, match)
		}
		findings[i].Secret = ""
		findings[i].Match = ""
		findings[i].Line = ""
		findings[i].Fragment = nil
	}
	return hits
}

// normalizeBoundarySlashes replaces single '/', '|', or '!' characters at token
// boundaries with their canonical digits ('7' or '1') so word-boundary regexes
// match when a boundary digit or vertical stroke is misread. Byte and rune
// offsets remain 1:1 with the input.
func normalizeBoundarySlashes(s string) (string, bool) {
	if !strings.ContainsAny(s, "/|!") {
		return s, false
	}
	buf := []byte(s)
	changed := false
	for i := 0; i < len(buf); i++ {
		var rep byte
		switch buf[i] {
		case '/':
			rep = '7'
		case '|', '!':
			rep = '1'
		default:
			continue
		}
		prevAlnum := i > 0 && isASCIIAlnum(buf[i-1])
		nextAlnum := i+1 < len(buf) && isASCIIAlnum(buf[i+1])
		prevPunct := i > 0 && (buf[i-1] == '/' || buf[i-1] == ':' || buf[i-1] == '|' || buf[i-1] == '!')
		nextPunct := i+1 < len(buf) && (buf[i+1] == '/' || buf[i+1] == '|' || buf[i+1] == '!')
		if (prevAlnum && !nextAlnum && !nextPunct) || (!prevAlnum && !prevPunct && nextAlnum) {
			buf[i] = rep
			changed = true
		}
	}
	if !changed {
		return s, false
	}
	return string(buf), true
}

func isASCIIAlnum(b byte) bool {
	return (b >= '0' && b <= '9') || (b >= 'A' && b <= 'Z') || (b >= 'a' && b <= 'z') || b == '_'
}

func findingToMatchOCR(rawRunes []rune, probe string, f *report.Finding, lineIdx []int, m *Matcher) (Match, bool) {
	if f == nil {
		return Match{}, false
	}
	bareID := strings.TrimSpace(f.RuleID)
	if bareID == "" {
		return Match{}, false
	}
	ruleID := bareID
	if _, isLocal := m.localIDs[bareID]; !isLocal {
		ruleID = CatalogRuleID(bareID)
		bareID = strings.TrimPrefix(ruleID, gitleaksPrefix)
	}
	probeSecret := f.Secret
	if probeSecret == "" {
		probeSecret = f.Match
	}
	start, end := offsetsFromFinding(probe, f, lineIdx, probeSecret)
	if end <= start || start < 0 || end > len(rawRunes) {
		return Match{}, false
	}
	secret := string(rawRunes[start:end])
	if looksLikePathOrURL(secret) {
		return Match{}, false
	}
	confusions := m.ocr.confusions
	if requirement, ok := m.requirements[bareID]; ok && !acceptOCRRequirements(requirement, secret, confusions) {
		return Match{}, false
	}
	if isOCRPlaceholder(m.placeholders, secret, confusions) {
		return Match{}, false
	}
	title := strings.TrimSpace(f.Description)
	if t := m.titles[bareID]; t != "" {
		title = t
	}
	sev := "critical"
	if s := m.severities[bareID]; s != "" {
		sev = s
	}
	_, isVendor := m.vendorIDs[bareID]
	return Match{
		RuleID:       ruleID,
		Title:        title,
		Severity:     sev,
		Start:        start,
		End:          end,
		GenericShape: GenericShape(secret),
		Fingerprint:  m.fingerprinter.Fingerprint(secret),
		Source:       SourceShapeRule,
		vendor:       isVendor,
	}, true
}

func looksLikePathOrURL(secret string) bool {
	if strings.Contains(secret, "//") {
		return true
	}
	if strings.Count(secret, "/") < 2 {
		return false
	}
	parts := strings.Split(secret, "/")
	lowerWords := 0
	for _, p := range parts {
		if len(p) >= 2 && isLowerPathSegment(p) {
			lowerWords++
		}
	}
	return lowerWords >= 2
}

func isLowerPathSegment(s string) bool {
	for _, r := range s {
		if (r < 'a' || r > 'z') && r != '_' && r != '-' && r != '.' {
			return false
		}
	}
	return true
}

func harvestMatchesOCR(ctx context.Context, m *Matcher, s string) []Match {
	if m == nil || s == "" || m.ocr == nil {
		return nil
	}
	values := collectHarvestValues(ctx, m)
	if len(values) == 0 {
		return nil
	}
	c := m.ocr.confusions
	canonS := c.canonicalize(s)
	var hits []Match
	for _, v := range values {
		for _, n := range harvestNeedles(v.Secret, MinNeedleRunes(v.NonDisclosable)) {
			hits = append(hits, needleMatchesOCR(s, canonS, n, v, c)...)
		}
	}
	return hits
}

func needleMatchesOCR(s, canonS string, n needle, v HarvestedValue, c ocrConfusionCatalog) []Match {
	canonNeedle := c.canonicalize(n.text)
	if canonNeedle == "" || len(canonNeedle) > len(canonS) {
		return nil
	}
	var hits []Match
	byteAt := 0
	needleRunes := utf8.RuneCountInString(canonNeedle)
	for occurrences := 0; v.NonDisclosable || occurrences < maxHarvestOccurrences; {
		rel := strings.Index(canonS[byteAt:], canonNeedle)
		if rel < 0 {
			break
		}
		at := byteAt + rel
		byteAt = at + len(canonNeedle)
		if !v.NonDisclosable && wordBounded(s, at, byteAt) {
			continue
		}
		occurrences++
		start := utf8.RuneCountInString(canonS[:at])
		reference := ""
		if n.whole {
			reference = v.Reference
		}
		hits = append(hits, Match{
			RuleID:         v.RuleID,
			Title:          v.Title,
			Severity:       "high",
			Start:          start,
			End:            start + needleRunes,
			GenericShape:   GenericShape(n.plain),
			Fingerprint:    v.Fingerprint,
			VarName:        v.Name,
			Container:      v.Container,
			Source:         v.Source,
			Reference:      reference,
			Retired:        v.Retired,
			NonDisclosable: v.NonDisclosable,
		})
	}
	return hits
}
