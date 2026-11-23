package secretmatch

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"slices"
	"strings"
)

// HarvestDigest summarizes session-harvested exact-match evidence without exposing secret values.
func (m *Matcher) HarvestDigest(ctx context.Context) [32]byte {
	if m == nil || m.harvestSource == nil {
		return [32]byte{}
	}
	items := slices.Clone(m.harvestSource(ctx))
	if len(items) == 0 {
		return [32]byte{}
	}
	slices.SortFunc(items, func(a, b HarvestedValue) int {
		if c := strings.Compare(string(a.Fingerprint), string(b.Fingerprint)); c != 0 {
			return c
		}
		if c := strings.Compare(a.Secret, b.Secret); c != 0 {
			return c
		}
		if c := strings.Compare(a.Reference, b.Reference); c != 0 {
			return c
		}
		return strings.Compare(a.RuleID, b.RuleID)
	})
	h := sha256.New()
	writeField := func(s string) {
		var lenBuf [4]byte
		binary.BigEndian.PutUint32(lenBuf[:], uint32(len(s))) // #nosec G115 -- secret sizes are bounded by MaxSecretBytes.
		_, _ = h.Write(lenBuf[:])
		_, _ = h.Write([]byte(s))
	}
	for _, v := range items {
		writeField(v.Name)
		writeField(v.Container)
		writeField(v.Secret)
		writeField(string(v.Fingerprint))
		writeField(v.RuleID)
		writeField(v.Title)
		writeField(string(v.Source))
		writeField(v.Reference)
		var flags [2]byte
		if v.Retired {
			flags[0] = 1
		}
		if v.NonDisclosable {
			flags[1] = 1
		}
		_, _ = h.Write(flags[:])
	}
	var sum [32]byte
	copy(sum[:], h.Sum(nil))
	return sum
}

// ClassificationRevision identifies current exact-value corrections for cache keys.
func (m *Matcher) ClassificationRevision(ctx context.Context) string {
	if m == nil || m.ignoredSource == nil {
		return ""
	}
	var fingerprints []SecretFingerprint
	for fp, active := range m.ignoredSource(ctx) {
		if active {
			fingerprints = append(fingerprints, fp)
		}
	}
	return FingerprintDigest(fingerprints)
}

// Ignored includes catalog placeholders and active exceptions; protected evidence wins.
func (m *Matcher) Ignored(ctx context.Context, value string) bool {
	if m == nil || m.fingerprinter == nil {
		return false
	}
	if m.Protected(ctx, value) {
		return false
	}
	if m.placeholders.IsPlaceholder(value) {
		return true
	}
	return m.classifications(ctx)[m.fingerprinter.Fingerprint(value)]
}

// ClassificationSnapshot binds a release to the exceptions used during screening.
func (m *Matcher) ClassificationSnapshot(ctx context.Context) map[SecretFingerprint]bool {
	out := map[SecretFingerprint]bool{}
	if m != nil && m.ignoredSource != nil {
		for fp, active := range m.ignoredSource(ctx) {
			if active {
				out[fp] = true
			}
		}
	}
	return out
}

// CheckClassification refuses a release prepared under withdrawn exceptions.
func (m *Matcher) CheckClassification(ctx context.Context, previous map[SecretFingerprint]bool) error {
	current := m.ClassificationSnapshot(ctx)
	for fp := range previous {
		if !current[fp] {
			return NewAskFault(FaultStageRaise, errors.New("secret classifications changed during review; retry the request"))
		}
	}
	return nil
}

// Protected detects non-disclosable evidence inside a proposed public value.
func (m *Matcher) Protected(ctx context.Context, value string) bool {
	if m == nil {
		return false
	}
	for _, hit := range m.harvestMatches(ctx, maskReferenceTokens(value).text) {
		if hit.NonDisclosable {
			return true
		}
	}
	return false
}

type classificationContextKey struct{}
type classificationContext struct {
	matcher *Matcher
	values  map[SecretFingerprint]bool
}

// WithClassifications shares one snapshot across fields in a screening pass.
func (m *Matcher) WithClassifications(ctx context.Context, values map[SecretFingerprint]bool) context.Context {
	return context.WithValue(ctx, classificationContextKey{}, classificationContext{matcher: m, values: values})
}

func (m *Matcher) classifications(ctx context.Context) map[SecretFingerprint]bool {
	if snapshot, ok := ctx.Value(classificationContextKey{}).(classificationContext); ok && snapshot.matcher == m {
		return snapshot.values
	}
	if m == nil || m.ignoredSource == nil {
		return nil
	}
	return m.ignoredSource(ctx)
}
