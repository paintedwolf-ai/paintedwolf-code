package secretmatch

import (
	"context"
	"strings"
	"unicode/utf8"
)

// HarvestRuleID marks a match whose only evidence is container membership.
const HarvestRuleID = "container-value"

// ManagedRuleID marks exact-match evidence from a managed capability.
const ManagedRuleID = "managed-secret"

// ManagedRuleTitle is the presentation name for ManagedRuleID.
const ManagedRuleTitle = "A managed secret"

// IsManagedRule reports whether ruleID is ManagedRuleID.
func IsManagedRule(ruleID string) bool { return ruleID == ManagedRuleID }

// maxHarvestOccurrences bounds the spans one value can contribute per screen.
const maxHarvestOccurrences = 16

// MinHarvestNeedleRunes limits incidental matches from inferred credentials.
const MinHarvestNeedleRunes = 8

// MinManagedSecretRunes admits four-character credentials while excluding
// shorter values that collide frequently with ordinary text.
const MinManagedSecretRunes = 4

// MinNeedleRunes returns the exact-match floor for one evidence class.
func MinNeedleRunes(nonDisclosable bool) int {
	if nonDisclosable {
		return MinManagedSecretRunes
	}
	return MinHarvestNeedleRunes
}

// MaxSecretBytes bounds one protected value across admission surfaces.
const MaxSecretBytes = 64 << 10

// HarvestedValue carries protected exact-match evidence.
type HarvestedValue struct {
	Name        string
	Container   string
	Secret      string
	Fingerprint SecretFingerprint
	// RuleID and Title preserve the original evidence identity.
	RuleID string
	Title  string
	Source RedactionSource
	// Reference is the managed capability that may replace this value.
	Reference string
	// Retired marks managed bytes whose capability no longer resolves.
	Retired bool
	// NonDisclosable excludes the value from model requests.
	NonDisclosable bool
}

// HarvestSource supplies exact-match evidence for one screen.
type HarvestSource func(ctx context.Context) []HarvestedValue

// IgnoredSource supplies active public-value exceptions.
type IgnoredSource func(context.Context) map[SecretFingerprint]bool

// Remembered is one confirmed value offered to the evidence base.
type Remembered struct {
	// Secret is the exact-match input.
	Secret string
	// Name is the field or binding it was confirmed in.
	Name string
	// Origin is human-readable provenance: the surface that carried it.
	Origin string
	RuleID string
	Title  string
	Source RedactionSource
	// Reference is the managed capability that may replace this value. It is
	// absent where the screen's audience may not spend the capability.
	Reference string
	// Retired marks managed bytes whose capability no longer resolves for
	// anyone, independent of the audience.
	Retired bool
	// NonDisclosable excludes the value from model requests.
	NonDisclosable bool
}

// RememberFunc records confirmed values against a session tree.
type RememberFunc func(rootSessionID string, values []Remembered)

// SetRemember installs the sink for rule-confirmed values.
func (m *Matcher) SetRemember(fn RememberFunc) {
	if m != nil {
		m.remember = fn
	}
}

// Remember adds rule-confirmed values to exact-match evidence.
func (m *Matcher) Remember(rootSessionID string, values []Remembered) {
	if m == nil || m.remember == nil || len(values) == 0 {
		return
	}
	m.remember(rootSessionID, values)
}

// SetHarvestSource installs container-evidence matching beside the shape rules.
func (m *Matcher) SetHarvestSource(fn HarvestSource) {
	if m != nil {
		m.harvestSource = fn
	}
}

// SetIgnoredSource installs the public-value exception source.
func (m *Matcher) SetIgnoredSource(fn IgnoredSource) {
	if m != nil {
		m.ignoredSource = fn
	}
}

type screeningValuesKey struct{}

// WithScreeningValues adds request-local evidence without changing shared memory.
func WithScreeningValues(ctx context.Context, values []Remembered) context.Context {
	return context.WithValue(ctx, screeningValuesKey{}, values)
}

// needle is one spelling of protected bytes.
type needle struct {
	text string
	// plain is the unencoded text this spelling carries.
	plain string
	// whole marks a spelling of the entire value, which its reference may replace.
	whole bool
}

// harvestNeedles spells the value, and each eligible line of a multi-line
// value, in every form a serializer could give it.
func harvestNeedles(secret string, floor int) []needle {
	if utf8.RuneCountInString(secret) < floor {
		return nil
	}
	seen := map[string]struct{}{}
	var needles []needle
	spell := func(plain string, whole bool) {
		for _, text := range append([]string{plain}, EncodedForms(plain)...) {
			if _, dup := seen[text]; dup {
				continue
			}
			seen[text] = struct{}{}
			needles = append(needles, needle{text: text, plain: plain, whole: whole})
		}
	}
	spell(secret, true)
	if !strings.ContainsAny(secret, "\r\n") {
		return needles
	}
	for _, line := range strings.FieldsFunc(secret, func(r rune) bool { return r == '\n' || r == '\r' }) {
		line = strings.TrimSpace(line)
		if utf8.RuneCountInString(line) < floor {
			continue
		}
		spell(line, false)
	}
	return needles
}

// harvestMatches finds standalone occurrences of protected values.
func (m *Matcher) harvestMatches(ctx context.Context, s string) []Match {
	if m == nil || s == "" {
		return nil
	}
	local, _ := ctx.Value(screeningValuesKey{}).([]Remembered)
	current := make(map[string]bool, len(local))
	for _, value := range local {
		current[value.Secret] = true
	}
	var values []HarvestedValue
	if m.harvestSource != nil {
		for _, value := range m.harvestSource(ctx) {
			if IsManagedRule(value.RuleID) && current[value.Secret] {
				continue
			}
			values = append(values, value)
		}
	}
	for _, value := range local {
		if m.fingerprinter == nil {
			continue
		}
		values = append(values, HarvestedValue{
			Secret: value.Secret, Name: value.Name, Container: value.Origin,
			RuleID: value.RuleID, Title: value.Title, Source: value.Source,
			Reference: value.Reference, Retired: value.Retired, NonDisclosable: value.NonDisclosable,
			Fingerprint: m.fingerprinter.Fingerprint(value.Secret),
		})
	}
	var hits []Match
	for _, v := range values {
		for _, n := range harvestNeedles(v.Secret, MinNeedleRunes(v.NonDisclosable)) {
			hits = append(hits, m.needleMatches(s, n, v)...)
		}
	}
	return hits
}

func (m *Matcher) needleMatches(s string, n needle, v HarvestedValue) []Match {
	if n.text == "" || len(n.text) > len(s) {
		return nil
	}
	var hits []Match
	byteAt := 0
	for occurrences := 0; v.NonDisclosable || occurrences < maxHarvestOccurrences; {
		rel := strings.Index(s[byteAt:], n.text)
		if rel < 0 {
			break
		}
		at := byteAt + rel
		byteAt = at + len(n.text)
		if !v.NonDisclosable && wordBounded(s, at, byteAt) {
			continue
		}
		occurrences++
		start := utf8.RuneCountInString(s[:at])
		// A line of a multi-line value is not the value its reference names.
		reference := ""
		if n.whole {
			reference = v.Reference
		}
		hits = append(hits, Match{
			RuleID:   v.RuleID,
			Title:    v.Title,
			Severity: "high",
			Start:    start,
			End:      start + utf8.RuneCountInString(n.text),
			// Exact matches need a synthetic shape for approval copy.
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

// wordBounded reports whether a match belongs to a larger token.
func wordBounded(s string, start, end int) bool {
	return isWordByteBefore(s, start) || isWordByteAt(s, end)
}

func isWordByteBefore(s string, at int) bool {
	if at <= 0 {
		return false
	}
	return isWordByte(s[at-1])
}

func isWordByteAt(s string, at int) bool {
	if at >= len(s) {
		return false
	}
	return isWordByte(s[at])
}

func isWordByte(b byte) bool {
	switch {
	case b >= 'a' && b <= 'z', b >= 'A' && b <= 'Z', b >= '0' && b <= '9':
		return true
	case b == '_':
		return true
	default:
		return b >= utf8.RuneSelf
	}
}

// dropIgnored removes public-value exceptions; protected matches stay.
func (m *Matcher) dropIgnored(ctx context.Context, hits []Match) []Match {
	if m == nil || m.ignoredSource == nil || len(hits) == 0 {
		return hits
	}
	ignored := m.classifications(ctx)
	if len(ignored) == 0 {
		return hits
	}
	out := hits[:0]
	for _, hit := range hits {
		if !hit.NonDisclosable && hit.Fingerprint != "" && ignored[hit.Fingerprint] {
			continue
		}
		out = append(out, hit)
	}
	return out
}

// propagateNonDisclosable marks matches overlapping protected exact values and
// copies the managed reference onto them for presentation. Retired bytes keep
// no reference: theirs no longer resolves.
func propagateNonDisclosable(hits []Match) {
	for i := range hits {
		for j := range hits {
			if !overlaps(hits[i], hits[j]) {
				continue
			}
			if hits[j].NonDisclosable {
				hits[i].NonDisclosable = true
			}
			if hits[i].Reference == "" && !hits[i].Retired && hits[j].Reference != "" {
				hits[i].Reference = hits[j].Reference
			}
		}
	}
}

// mergeContainerEvidence preserves binding provenance on overlapping hits.
func mergeContainerEvidence(hits []Match) []Match {
	for i := range hits {
		if hits[i].Source != SourceShapeRule || hits[i].VarName != "" {
			continue
		}
		for j := range hits {
			if hits[j].Source != SourceContainerHarvest {
				continue
			}
			if overlaps(hits[i], hits[j]) {
				hits[i].VarName = hits[j].VarName
				hits[i].Container = hits[j].Container
				break
			}
		}
	}
	return hits
}
