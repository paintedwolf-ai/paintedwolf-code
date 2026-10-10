package security

import (
	"bytes"
	"context"
	"fmt"

	"github.com/lycaon/lycaon/internal/observability"
	"github.com/lycaon/lycaon/internal/projectignore"
	"github.com/lycaon/lycaon/internal/protectedpath"
	"github.com/lycaon/lycaon/internal/secretharvest"
	"github.com/lycaon/lycaon/internal/secretmatch"
)

func (b *Runtime) LoadMatcher(override *secretmatch.Matcher) (*secretmatch.Matcher, error) {
	if b.Matcher != nil {
		return b.Matcher, nil
	}
	if override != nil {
		b.Matcher = override
		fingerprinter, err := secretmatch.NewFingerprinter(bytes.Repeat([]byte{0x5a}, 32))
		if err != nil {
			return nil, fmt.Errorf("test secret fingerprint key: %w", err)
		}
		b.Matcher.SetFingerprinter(fingerprinter)
		b.InstallEvidence(b.Matcher, fingerprinter)
		return b.Matcher, nil
	}
	m, err := secretmatch.BuildMatcher(secretmatch.Bundled())
	if err != nil {
		return nil, fmt.Errorf("secret patterns: %w", err)
	}
	fingerprinter, err := secretmatch.OpenFingerprinter()
	if err != nil {
		return nil, fmt.Errorf("secret fingerprint key: %w", err)
	}
	m.SetFingerprinter(fingerprinter)
	b.InstallEvidence(m, fingerprinter)
	b.Matcher = m
	return m, nil
}

// InstallEvidence binds matcher evidence to the managed secret stores.
func (b *Runtime) InstallEvidence(m *secretmatch.Matcher, fp *secretmatch.Fingerprinter) {
	b.Matcher, b.Fingerprinter = m, fp
	b.Harvest = secretharvest.NewRuntime(fp)
	harvest := b.Harvest
	harvest.SetProjectResolver(func(rootSessionID string) string {
		if b.sessions == nil {
			return ""
		}
		sess, err := b.sessions.Get(context.Background(), rootSessionID)
		if err != nil || sess == nil {
			return ""
		}
		return sess.ProjectID
	})
	m.SetHarvestSource(func(ctx context.Context) []secretmatch.HarvestedValue {
		var values []secretharvest.Value
		attribution := secretmatch.AskAttributionFrom(ctx)
		switch {
		case attribution.RootSessionID != "":
			values = harvest.ValuesFor(attribution.RootSessionID)
		case attribution.ProjectID != "":
			values = harvest.ValuesForProject(attribution.ProjectID)
		default:
			values = harvest.AllValues()
		}
		var managed []secretmatch.Remembered
		if b.Capabilities != nil {
			managed = b.Capabilities.DurableScreeningValues(secretScreenProject(ctx))
		}
		protected := make(map[string]bool, len(managed))
		out := make([]secretmatch.HarvestedValue, 0, len(values)+len(managed))
		for _, v := range managed {
			protected[v.Secret] = true
			out = append(out, secretmatch.HarvestedValue{
				Name: v.Name, Container: v.Origin, Secret: v.Secret,
				Fingerprint: fp.Fingerprint(v.Secret), RuleID: v.RuleID,
				Title: v.Title, Source: v.Source, Retired: v.Retired, NonDisclosable: true,
			})
		}
		for _, v := range values {
			// Retained protection takes precedence over remembered capability
			// references and over any weaker evidence for the same bytes.
			if protected[v.Secret()] && (secretmatch.IsManagedRule(v.RuleID) || !v.NonDisclosable) {
				continue
			}
			out = append(out, secretmatch.HarvestedValue{
				Name: v.Name, Container: v.Container,
				Secret: v.Secret(), Fingerprint: v.Fingerprint,
				RuleID: v.RuleID, Title: v.Title, Source: v.Source, Reference: v.Reference,
				Retired: v.Retired, NonDisclosable: v.NonDisclosable,
			})
		}
		return out
	})
	b.Ignores = &projectignore.SecretService{
		Trusted: func(ctx context.Context, projectID string) bool {
			if b.projects == nil || b.trust == nil {
				return false
			}
			p, err := b.projects.Get(ctx, projectID)
			return err == nil && b.trust.Applies(protectedpath.SurfaceScanConfig, *p)
		},
		Roots: func(ctx context.Context, projectID string) ([]projectignore.Root, error) {
			if b.projects == nil {
				return nil, projectignore.ErrUnavailable
			}
			p, err := b.projects.Get(ctx, projectID)
			if err != nil {
				return nil, err
			}
			roots := make([]projectignore.Root, 0, len(p.Roots))
			for _, r := range p.Roots {
				roots = append(roots, projectignore.Root{ID: r.ID, Path: r.Path})
			}
			return roots, nil
		},
		Protected: func(ctx context.Context, projectID, value string) bool {
			attr := secretmatch.AskAttributionFrom(ctx)
			attr.ProjectID = projectID
			return m.Protected(secretmatch.WithAskAttribution(ctx, attr), value)
		},
	}
	m.SetIgnoredSource(func(ctx context.Context) map[secretmatch.SecretFingerprint]bool {
		out := map[secretmatch.SecretFingerprint]bool{}
		for _, entry := range b.Ignores.ActiveValues(ctx, secretScreenProject(ctx)) {
			out[fp.Fingerprint(entry.Value)] = true
		}
		return out
	})
	remember := func(rootSessionID string, values []secretmatch.Remembered) {
		harvest.Remember(rootSessionID, values...)
	}
	m.SetRemember(remember)
	if b.remember != nil {
		b.remember(remember)
	}
	// New evidence invalidates earlier screens.
	harvest.OnGrowth(func(rootSessionID string, generation uint64) {
		if b.sweep == nil {
			return
		}
		go b.sweep(context.WithoutCancel(context.Background()), rootSessionID, generation)
	})
	// Capture tags correlate redacted occurrences.
	if b.releaseCaptureRedactor != nil {
		b.releaseCaptureRedactor()
	}
	b.releaseCaptureRedactor = observability.SetCaptureRedactor(func(text string) string {
		redacted, spans := m.RedactLabeledSpansWhere(context.Background(), "", text, nil)
		return secretmatch.ApplyPseudonyms(redacted, spans)
	})
}
