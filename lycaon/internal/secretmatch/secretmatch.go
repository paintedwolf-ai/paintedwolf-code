// Package secretmatch compiles secret catalogs and screens outbound plaintext.
// Matches contain only redaction-safe metadata.
package secretmatch

import (
	"context"
	"sort"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/zricethezav/gitleaks/v8/detect"
	"github.com/zricethezav/gitleaks/v8/report"
)

// MaxScreenWindowBytes bounds one detector pass. Longer inputs use overlapping windows.
const MaxScreenWindowBytes = 1 << 20

// screenWindowOverlapBytes keeps boundary-spanning matches inside one window.
const screenWindowOverlapBytes = 64 << 10

const gitleaksPrefix = "gitleaks:"

// CatalogRuleID namespaces a detector catalog rule id as findings report it.
func CatalogRuleID(id string) string {
	if id == "" || strings.HasPrefix(id, gitleaksPrefix) {
		return id
	}
	return gitleaksPrefix + id
}

// Match is one redaction-safe hit. The matched substring is never stored.
type Match struct {
	RuleID     string
	Title      string
	Severity   string
	Start, End int // rune offsets into the screened input
	// GenericShape contains synthetic character classes and run lengths.
	GenericShape string
	// Fingerprint is a host-only identity for the matched bytes.
	Fingerprint SecretFingerprint
	// VarName and Container identify binding provenance.
	VarName   string
	Container string
	// Source names which evidence lens produced the hit.
	Source RedactionSource
	// Reference is a managed-secret capability safe for the model to retain.
	Reference string
	// Retired marks managed bytes whose capability no longer resolves: a
	// replaced version or a revoked capability.
	Retired bool
	// NonDisclosable excludes the match from model requests.
	NonDisclosable bool
	vendor         bool
}

// Matcher holds a compiled detector and attribution metadata.
type Matcher struct {
	detector *detect.Detector
	// Metadata keys use bare upstream rule IDs.
	titles        map[string]string
	severities    map[string]string
	localIDs      map[string]struct{}
	vendorIDs     map[string]struct{}
	requirements  map[string]kingfisherRequirements
	fingerprinter *Fingerprinter
	inert         bool
	// harvestSource adds container-evidence matches beside the shape rules.
	harvestSource HarvestSource
	// ignoredSource supplies active public-value exceptions.
	ignoredSource IgnoredSource
	// remember admits rule-confirmed values to the evidence base.
	remember RememberFunc
	// screenCache remembers catalog-pass spans by content digest.
	screenCache *screenCache
	// Zero window sizes use the defaults.
	windowBytes  int
	overlapBytes int
	// catalogVersion identifies the vendored rule set a screen ran against.
	catalogVersion string
	// placeholders are documented example values a shape rule never reports.
	placeholders placeholderCatalog
}

// CatalogVersion is empty when no vendor catalog is loaded.
func (m *Matcher) CatalogVersion() string {
	if m == nil {
		return ""
	}
	return m.catalogVersion
}

// SetScreenWindow configures detector windowing.
func (m *Matcher) SetScreenWindow(windowBytes, overlapBytes int) {
	if m == nil {
		return
	}
	m.windowBytes, m.overlapBytes = windowBytes, overlapBytes
}

// screenWindowBytes returns the byte budget for one detector pass.
func (m *Matcher) screenWindowBytes() int {
	if m != nil && m.windowBytes > 0 {
		return m.windowBytes
	}
	return MaxScreenWindowBytes
}

// screenOverlap returns the bytes repeated between adjacent windows.
func (m *Matcher) screenOverlap() int {
	if m != nil && m.overlapBytes > 0 {
		return m.overlapBytes
	}
	return screenWindowOverlapBytes
}

// SetFingerprinter installs the device-keyed identity function.
func (m *Matcher) SetFingerprinter(f *Fingerprinter) {
	if m != nil {
		m.fingerprinter = f
	}
}

// NewInertMatcher returns a matcher with no findings.
func NewInertMatcher() *Matcher {
	return &Matcher{inert: true}
}

// Inert reports whether Screen can produce findings.
func (m *Matcher) Inert() bool {
	return m == nil || (m.inert || m.detector == nil) && m.harvestSource == nil
}

// Screen finds secret-pattern matches across the full input.
func (m *Matcher) Screen(s string) []Match {
	return m.ScreenContext(context.Background(), s)
}

// RedactString masks the union of detected spans.
func (m *Matcher) RedactString(ctx context.Context, s string) string {
	out, _ := redactMatches(s, m.screenRaw(ctx, s))
	return out
}

// ScreenLabeledContext applies a field label while preserving value offsets.
func (m *Matcher) ScreenLabeledContext(ctx context.Context, label, value string) []Match {
	return dedupeAndSort(m.screenLabeledRaw(ctx, label, value))
}

// screenLabeledRaw screens label and value but returns value-relative hits.
func (m *Matcher) screenLabeledRaw(ctx context.Context, label, value string) []Match {
	if strings.TrimSpace(label) == "" {
		return m.screenRaw(ctx, value)
	}
	prefix := label + "="
	prefixRunes := utf8.RuneCountInString(prefix)
	hits := m.screenRaw(ctx, prefix+value)
	out := make([]Match, 0, len(hits))
	for _, hit := range hits {
		if hit.Start < prefixRunes || hit.End <= prefixRunes {
			continue
		}
		hit.Start -= prefixRunes
		hit.End -= prefixRunes
		out = append(out, hit)
	}
	return out
}

// RedactLabeled redacts a field with deterministic label context.
func (m *Matcher) RedactLabeled(ctx context.Context, label, value string) string {
	out, _ := redactMatches(value, m.screenLabeledRaw(ctx, label, value))
	return out
}

// RedactLabeledSpansWhere redacts matches accepted by keep and returns their
// markers. A nil keep accepts every match.
func (m *Matcher) RedactLabeledSpansWhere(
	ctx context.Context,
	label, value string,
	keep func(Match) bool,
) (string, []RedactionSpan) {
	hits := m.screenLabeledRaw(ctx, label, value)
	if keep != nil {
		selected := hits[:0]
		for _, hit := range hits {
			if keep(hit) {
				selected = append(selected, hit)
			}
		}
		hits = selected
	}
	return redactMatches(value, hits)
}

// ScreenContext returns ranked, non-overlapping findings.
func (m *Matcher) ScreenContext(ctx context.Context, s string) []Match {
	return dedupeAndSort(m.screenRaw(ctx, s))
}

// screenRaw returns unmerged catalog and exact-match findings. Both lenses read
// the input with reference tokens masked.
func (m *Matcher) screenRaw(ctx context.Context, s string) []Match {
	if m == nil || ctx == nil {
		return nil
	}
	mask := maskReferenceTokens(s)
	hits := m.catalogMatches(ctx, mask.text)
	hits = append(hits, m.harvestMatches(ctx, mask.text)...)
	hits = mask.outside(hits)
	propagateNonDisclosable(hits)
	return m.dropIgnored(ctx, mergeContainerEvidence(hits))
}

// runCatalog scans every detector window.
func (m *Matcher) runCatalog(ctx context.Context, s string) []Match {
	if len(s) <= m.screenWindowBytes() {
		return m.screenWindow(ctx, s, 0)
	}
	var hits []Match
	for _, w := range screenWindows(s, m.screenWindowBytes(), m.screenOverlap()) {
		if ctx.Err() != nil {
			break
		}
		hits = append(hits, m.screenWindow(ctx, s[w.start:w.end], w.runeOffset)...)
	}
	return hits
}

// dedupeAndSort ranks one winner per overlapping span.
func dedupeAndSort(hits []Match) []Match {
	if len(hits) == 0 {
		return nil
	}
	hits = dedupeOverlaps(hits)
	sort.SliceStable(hits, func(i, j int) bool {
		ri, rj := SeverityRank(hits[i].Severity), SeverityRank(hits[j].Severity)
		if ri != rj {
			return ri > rj
		}
		if hits[i].Start != hits[j].Start {
			return hits[i].Start < hits[j].Start
		}
		return hits[i].RuleID < hits[j].RuleID
	})
	return hits
}

// screenWindow shifts detector findings into whole-input coordinates.
func (m *Matcher) screenWindow(ctx context.Context, window string, runeOffset int) []Match {
	//nolint:staticcheck // The detector does not expose a replacement fragment.
	findings := m.detector.DetectContext(ctx, detect.Fragment{Raw: window})
	if len(findings) == 0 {
		return nil
	}
	lineIdx := buildLineIndex(window)
	hits := make([]Match, 0, len(findings))
	for i := range findings {
		if match, ok := findingToMatch(window, &findings[i], lineIdx, m); ok {
			match.Start += runeOffset
			match.End += runeOffset
			hits = append(hits, match)
		}
		// Clear transient secret-bearing fields.
		findings[i].Secret = ""
		findings[i].Match = ""
		findings[i].Line = ""
		findings[i].Fragment = nil
	}
	return hits
}

type screenSpan struct {
	start, end int
	runeOffset int
}

// screenWindows builds overlapping, line-aligned detector windows.
func screenWindows(s string, window, overlap int) []screenSpan {
	var out []screenSpan
	start, runeOffset := 0, 0
	for {
		end := windowEnd(s, start, window)
		out = append(out, screenSpan{start: start, end: end, runeOffset: runeOffset})
		if end == len(s) {
			return out
		}
		next := windowNextStart(s, start, end, overlap)
		runeOffset += utf8.RuneCountInString(s[start:next])
		start = next
	}
}

// windowEnd prefers a line break and preserves rune boundaries.
func windowEnd(s string, start, window int) int {
	end := start + window
	if end >= len(s) {
		return len(s)
	}
	if nl := strings.LastIndexByte(s[start:end], '\n'); nl > 0 {
		return start + nl + 1
	}
	for end > start && !utf8.RuneStart(s[end]) {
		end--
	}
	return end
}

// windowNextStart preserves overlap while advancing the window.
func windowNextStart(s string, start, end, overlap int) int {
	next := end - overlap
	if next <= start {
		return end
	}
	if nl := strings.IndexByte(s[next:end], '\n'); nl >= 0 && next+nl+1 < end {
		return next + nl + 1
	}
	for next < end && !utf8.RuneStart(s[next]) {
		next++
	}
	return next
}

func findingToMatch(s string, f *report.Finding, lineIdx []int, m *Matcher) (Match, bool) {
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

	title := strings.TrimSpace(f.Description)
	if t := m.titles[bareID]; t != "" {
		title = t
	}
	sev := "critical"
	if s := m.severities[bareID]; s != "" {
		sev = s
	}
	_, isVendor := m.vendorIDs[bareID]

	secret := f.Secret
	if secret == "" {
		secret = f.Match
	}
	if requirement, ok := m.requirements[bareID]; ok && !requirement.accept(secret) {
		return Match{}, false
	}
	if m.placeholders.IsPlaceholder(secret) {
		return Match{}, false
	}
	start, end := offsetsFromFinding(s, f, lineIdx, secret)
	if end <= start {
		return Match{}, false
	}
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

const genericShapePreviewRunes = 48

type genericShapeSegment struct {
	kind  string
	count int
}

// GenericShape describes a value by character class and run length, as a
// bounded synthetic example.
func GenericShape(secret string) string {
	runes := []rune(secret)
	if len(runes) == 0 {
		return ""
	}
	segments := make([]genericShapeSegment, 0, 4)
	for i := 0; i < len(runes); {
		start := i
		switch {
		case unicode.IsLetter(runes[i]) || unicode.IsDigit(runes[i]):
			hasLetter, hasDigit := false, false
			for i < len(runes) && (unicode.IsLetter(runes[i]) || unicode.IsDigit(runes[i])) {
				hasLetter = hasLetter || unicode.IsLetter(runes[i])
				hasDigit = hasDigit || unicode.IsDigit(runes[i])
				i++
			}
			kind := "letter"
			if hasLetter && hasDigit {
				kind = "alphanumeric"
			} else if hasDigit {
				kind = "digit"
			}
			segments = append(segments, genericShapeSegment{kind: kind, count: i - start})
		case unicode.IsSpace(runes[i]):
			for i < len(runes) && unicode.IsSpace(runes[i]) {
				i++
			}
			segments = append(segments, genericShapeSegment{kind: "whitespace", count: i - start})
		default:
			for i < len(runes) && !unicode.IsLetter(runes[i]) && !unicode.IsDigit(runes[i]) && !unicode.IsSpace(runes[i]) {
				i++
			}
			segments = append(segments, genericShapeSegment{kind: "symbol", count: i - start})
		}
	}
	patterns := map[string][]rune{
		"letter":       []rune("abc"),
		"digit":        []rune("123"),
		"alphanumeric": []rune("a1b2c3"),
		"symbol":       []rune("-_"),
		"whitespace":   []rune("␠"),
	}
	var example strings.Builder
	written := 0
	for _, segment := range segments {
		pattern := patterns[segment.kind]
		for i := 0; i < segment.count && written < genericShapePreviewRunes; i++ {
			example.WriteRune(pattern[i%len(pattern)])
			written++
		}
		if written == genericShapePreviewRunes {
			break
		}
	}
	if len(runes) > genericShapePreviewRunes {
		example.WriteRune('…')
	}
	example.WriteString(" (" + strconv.Itoa(len(runes)) + " characters)")
	return example.String()
}

func buildLineIndex(s string) []int {
	// lineIdx[i] = byte offset of the start of 0-based line i.
	idx := []int{0}
	for i := 0; i < len(s); i++ {
		if s[i] == '\n' {
			idx = append(idx, i+1)
		}
	}
	return idx
}

func offsetsFromFinding(s string, f *report.Finding, lineIdx []int, secret string) (start, end int) {
	// Locate the capture within its reported line first.
	if secret != "" && f.StartLine >= 0 && f.StartLine < len(lineIdx) {
		lineStart := lineIdx[f.StartLine]
		if i := strings.Index(s[lineStart:], secret); i >= 0 {
			bStart := lineStart + i
			bEnd := bStart + len(secret)
			return utf8.RuneCountInString(s[:bStart]), utf8.RuneCountInString(s[:bEnd])
		}
	}
	// Otherwise use column metadata (1-based columns, 0-based lines).
	if f.StartLine >= 0 && f.StartColumn > 0 && f.StartLine < len(lineIdx) {
		lineStart := lineIdx[f.StartLine]
		bStart := lineStart + f.StartColumn - 1
		if bStart >= 0 && bStart <= len(s) {
			bEnd := bStart
			if f.EndLine == f.StartLine && f.EndColumn > 0 {
				bEnd = lineStart + f.EndColumn - 1
			} else if secret != "" {
				bEnd = bStart + len(secret)
			}
			if bEnd > bStart && bEnd <= len(s) {
				return utf8.RuneCountInString(s[:bStart]), utf8.RuneCountInString(s[:bEnd])
			}
		}
	}
	// Locate the transient capture in the full input as a fallback.
	if secret != "" {
		if i := strings.Index(s, secret); i >= 0 {
			return utf8.RuneCountInString(s[:i]), utf8.RuneCountInString(s[:i+len(secret)])
		}
	}
	return 0, 0
}

// SeverityRank orders match severities, highest first.
func SeverityRank(s string) int {
	switch strings.TrimSpace(s) {
	case "critical":
		return 2
	case "high":
		return 1
	default:
		return 0
	}
}

// dedupeOverlaps keeps the preferred match for each overlapping span.
func dedupeOverlaps(hits []Match) []Match {
	if len(hits) <= 1 {
		return hits
	}
	kept := make([]Match, 0, len(hits))
	for _, h := range hits {
		merged := false
		for i := range kept {
			if !overlaps(kept[i], h) {
				continue
			}
			if prefer(h, kept[i]) {
				kept[i] = h
			}
			merged = true
			break
		}
		if !merged {
			kept = append(kept, h)
		}
	}
	return kept
}

func overlaps(a, b Match) bool {
	return a.Start < b.End && b.Start < a.End
}

func covers(a, b Match) bool {
	return a.Start <= b.Start && b.End <= a.End
}

// prefer reports whether a should replace b as the name for a span both cover.
// A managed capability names every span it covers, and a live capability names
// bytes a retired version shares with it.
func prefer(a, b Match) bool {
	am, bm := IsManagedRule(a.RuleID), IsManagedRule(b.RuleID)
	if am != bm {
		if am && covers(a, b) {
			return true
		}
		if bm && covers(b, a) {
			return false
		}
	}
	if am && bm && a.Retired != b.Retired {
		if !a.Retired && covers(a, b) {
			return true
		}
		if !b.Retired && covers(b, a) {
			return false
		}
	}
	if a.vendor != b.vendor {
		return !a.vendor
	}
	ra, rb := SeverityRank(a.Severity), SeverityRank(b.Severity)
	if ra != rb {
		return ra > rb
	}
	la, lb := a.End-a.Start, b.End-b.Start
	if la != lb {
		return la > lb
	}
	return a.RuleID < b.RuleID
}
