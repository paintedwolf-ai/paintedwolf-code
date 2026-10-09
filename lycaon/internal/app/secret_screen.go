package app

import (
	"context"

	"github.com/lycaon/lycaon/internal/secretmatch"
	"github.com/lycaon/lycaon/internal/secretmint"
	"github.com/lycaon/lycaon/pkg/api"
)

func (b sessionWiring) wireCredentialObservations() {
	if b.mgr == nil {
		return
	}
	b.mgr.SetCredentialSlotProvider(func(ctx context.Context, sess *api.Session) *secretmint.Inspector {
		if view := b.mgr.Catalog().ViewForSession(ctx, sess); view != nil {
			return view.CredentialSlots
		}
		return nil
	})
	b.mgr.SetSecretFingerprinter(b.security.Fingerprinter)
	b.mgr.SetIgnoredCredentialCandidate(func(ctx context.Context, projectID, value string) bool {
		return b.security.Matcher.Ignored(secretmatch.WithAskAttribution(ctx, secretmatch.AskAttribution{ProjectID: projectID}), value)
	})
	if b.security.Harvest != nil {
		harvest := b.security.Harvest
		b.mgr.SetHarvestedFingerprint(func(root string, fp secretmatch.SecretFingerprint) bool {
			return harvest.Has(root, fp)
		})
	}
}
