package secretmatch

import (
	"context"
	"errors"
)

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
