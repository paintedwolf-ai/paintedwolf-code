package app

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"

	"github.com/lycaon/lycaon/internal/curationctx"
	"github.com/lycaon/lycaon/internal/observability"
	"github.com/lycaon/lycaon/internal/projectignore"
	"github.com/lycaon/lycaon/internal/protectedpath"
	"github.com/lycaon/lycaon/internal/secretharvest"
	"github.com/lycaon/lycaon/internal/secretmatch"
	"github.com/lycaon/lycaon/internal/secretmint"
	"github.com/lycaon/lycaon/pkg/api"
)

func (b sessionWiring) loadSecretMatcher() (*secretmatch.Matcher, error) {
	if b.secretMatcher != nil {
		return b.secretMatcher, nil
	}
	if b.cfg.TestSecretMatcher != nil {
		b.secretMatcher = b.cfg.TestSecretMatcher
		fingerprinter, err := secretmatch.NewFingerprinter(bytes.Repeat([]byte{0x5a}, 32))
		if err != nil {
			return nil, fmt.Errorf("test secret fingerprint key: %w", err)
		}
		b.secretMatcher.SetFingerprinter(fingerprinter)
		b.wireSecretEvidence(b.secretMatcher, fingerprinter)
		return b.secretMatcher, nil
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
	b.wireSecretEvidence(m, fingerprinter)
	b.secretMatcher = m
	return m, nil
}

// wireSecretEvidence installs runtime evidence sources.
func (b sessionWiring) wireSecretEvidence(m *secretmatch.Matcher, fp *secretmatch.Fingerprinter) {
	b.secretFingerprinter = fp
	b.secretHarvest = secretharvest.NewRuntime(fp)
	harvest := b.secretHarvest
	harvest.SetProjectResolver(func(rootSessionID string) string {
		if b.store == nil {
			return ""
		}
		sess, err := b.store.Get(context.Background(), rootSessionID)
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
		if b.secretCaps != nil {
			managed = b.secretCaps.DurableScreeningValues(secretScreenProject(ctx))
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
	b.secretIgnores = &projectignore.SecretService{
		Trusted: func(ctx context.Context, projectID string) bool {
			if b.registry == nil || b.settingsSvc == nil || b.settingsSvc.TrustSurfaces == nil {
				return false
			}
			p, err := b.registry.Get(ctx, projectID)
			return err == nil && b.settingsSvc.TrustSurfaces.Applies(protectedpath.SurfaceScanConfig, *p)
		},
		Roots: func(ctx context.Context, projectID string) ([]projectignore.Root, error) {
			if b.registry == nil {
				return nil, projectignore.ErrUnavailable
			}
			p, err := b.registry.Get(ctx, projectID)
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
		for _, entry := range b.secretIgnores.ActiveValues(ctx, secretScreenProject(ctx)) {
			out[fp.Fingerprint(entry.Value)] = true
		}
		return out
	})
	remember := func(rootSessionID string, values []secretmatch.Remembered) {
		harvest.Remember(rootSessionID, values...)
	}
	m.SetRemember(remember)
	if b.mgr != nil {
		b.mgr.ToolPolicy.SetRememberSecrets(remember)
	}
	// New evidence invalidates earlier screens.
	harvest.OnGrowth(func(rootSessionID string, generation uint64) {
		if b.mgr == nil {
			return
		}
		go b.mgr.Transcript.SweepSessionTree(context.WithoutCancel(context.Background()), rootSessionID, generation)
	})
	// Capture tags correlate redacted occurrences.
	observability.SetCaptureRedactor(func(text string) string {
		redacted, spans := m.RedactLabeledSpansWhere(context.Background(), "", text, nil)
		return secretmatch.ApplyPseudonyms(redacted, spans)
	})
}

func (b sessionWiring) secretAskFunc() secretmatch.AskFunc {
	return func(ctx context.Context, finding secretmatch.Alert) (secretmatch.Resolution, error) {
		if b.toolRuntime == nil || b.toolRuntime.Executor == nil {
			return secretmatch.Resolution{}, secretmatch.NewAskFault(
				secretmatch.FaultStageScreenUnwired, nil)
		}
		// Recorded redactions also apply when approvals are disabled.
		if b.toolRuntime.ApprovalsDisabled(secretmatch.AskAttributionFrom(ctx).ProjectDir) {
			return b.toolRuntime.Executor.ResolveSecretScreenUnasked(ctx, finding)
		}
		resolution, err := b.toolRuntime.Executor.AskSecretScreen(ctx, finding)
		if err != nil {
			// Ask faults have no card or ledger entry.
			slog.ErrorContext(ctx, "outbound secret screen could not ask",
				"component", "secret_screen",
				"surface", string(finding.Surface),
				"session_id", finding.SessionID,
				"root_session_id", finding.RootSessionID,
				"rule_id", finding.RuleID,
				"error", err)
		}
		return resolution, err
	}
}

func (b sessionWiring) wireCredentialObservations() {
	if b.mgr == nil {
		return
	}
	b.mgr.ToolPolicy.SetCredentialSlotProvider(func(ctx context.Context, sess *api.Session) *secretmint.Inspector {
		if view := b.mgr.Catalog.ViewForSession(ctx, sess); view != nil {
			return view.CredentialSlots
		}
		return nil
	})
	b.mgr.ToolPolicy.SetSecretFingerprinter(b.secretFingerprinter)
	b.mgr.ToolPolicy.SetIgnoredCredentialCandidate(func(ctx context.Context, projectID, value string) bool {
		return b.secretMatcher.Ignored(secretmatch.WithAskAttribution(ctx, secretmatch.AskAttribution{ProjectID: projectID}), value)
	})
	if b.secretHarvest != nil {
		harvest := b.secretHarvest
		b.mgr.ToolPolicy.SetHarvestedFingerprint(func(root string, fp secretmatch.SecretFingerprint) bool {
			return harvest.Has(root, fp)
		})
	}
}

func secretScreenProject(ctx context.Context) string {
	if projectID := secretmatch.AskAttributionFrom(ctx).ProjectID; projectID != "" {
		return projectID
	}
	return curationctx.SessionFrom(ctx).ProjectID
}
