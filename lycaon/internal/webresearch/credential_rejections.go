package webresearch

import (
	"context"
	"crypto/sha256"
	"strings"
	"sync"
	"time"
)

// credentialRejection records that a provider refused one credential value.
type credentialRejection struct {
	digest     [sha256.Size]byte
	code       string
	observedAt time.Time
}

// credentialRejections holds rejected credentials by slot for the process
// lifetime. A different credential in the slot no longer matches.
type credentialRejections struct {
	mu     sync.Mutex
	bySlot map[string]credentialRejection
	now    func() time.Time
}

func newCredentialRejections() *credentialRejections {
	return &credentialRejections{bySlot: make(map[string]credentialRejection), now: time.Now}
}

func (c *credentialRejections) record(slot, credential, code string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.bySlot[slot] = credentialRejection{digest: sha256.Sum256([]byte(credential)), code: code, observedAt: c.now()}
}

func (c *credentialRejections) match(slot, credential string) (credentialRejection, bool) {
	if slot == "" || credential == "" {
		return credentialRejection{}, false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	rej, ok := c.bySlot[slot]
	if !ok || rej.digest != sha256.Sum256([]byte(credential)) {
		return credentialRejection{}, false
	}
	return rej, true
}

func (c *credentialRejections) clear(slot string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.bySlot, slot)
}

func credentialSlot(entry CatalogEntry) string {
	if entry.CredentialSlot != "" {
		return entry.CredentialSlot
	}
	return entry.OptionalCredentialSlot
}

// credentialRejection reports whether the provider's resolved credential was
// rejected.
func (r *Registry) credentialRejection(providerID string, s Settings) (credentialRejection, bool) {
	if r == nil || r.catalog == nil {
		return credentialRejection{}, false
	}
	entry, ok := r.catalog.Entry(providerID)
	if !ok {
		return credentialRejection{}, false
	}
	return r.rejections.match(credentialSlot(entry), strings.TrimSpace(s.Keys[providerID]))
}

func (r *Registry) providerLabels(ids []string) []string {
	out := make([]string, 0, len(ids))
	for _, id := range ids {
		label := id
		if r != nil && r.catalog != nil {
			if entry, ok := r.catalog.Entry(id); ok {
				label = entry.Label
			}
		}
		out = append(out, label)
	}
	return out
}

// credentialGuardProvider stops dialing a provider whose current credential
// was rejected and records new rejections.
type credentialGuardProvider struct {
	inner      SearchProvider
	slot       string
	rejections *credentialRejections
}

func (p *credentialGuardProvider) ID() string { return p.inner.ID() }

func (p *credentialGuardProvider) Kind() ProviderKind { return p.inner.Kind() }

func (p *credentialGuardProvider) Configured(s Settings) bool { return p.inner.Configured(s) }

func (p *credentialGuardProvider) Search(ctx context.Context, s Settings, query string, maxResults int) providerOutcome {
	key := strings.TrimSpace(s.Keys[p.inner.ID()])
	if rej, ok := p.rejections.match(p.slot, key); ok {
		return providerOutcome{
			providerID: p.inner.ID(),
			reason:     string(ErrorCredentialRejected),
			detail:     "credential rejected " + rej.code,
			errorCode:  rej.code,
		}
	}
	out := p.inner.Search(ctx, s, query, maxResults)
	if out.reason == string(ErrorCredentialRejected) && key != "" {
		p.rejections.record(p.slot, key, out.errorCode)
	}
	return out
}
