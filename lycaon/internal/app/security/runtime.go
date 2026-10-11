package security

import (
	"context"
	"github.com/lycaon/lycaon/internal/authzcontext"
	"github.com/lycaon/lycaon/internal/curationctx"
	"github.com/lycaon/lycaon/internal/db"
	"github.com/lycaon/lycaon/internal/presence"
	"github.com/lycaon/lycaon/internal/project"
	"github.com/lycaon/lycaon/internal/projectignore"
	"github.com/lycaon/lycaon/internal/secretcap"
	"github.com/lycaon/lycaon/internal/secretharvest"
	"github.com/lycaon/lycaon/internal/secretmatch"
	"github.com/lycaon/lycaon/internal/secretspan"
	"github.com/lycaon/lycaon/internal/session/approvalstate"
	"github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/internal/settings"
)

type Runtime struct {
	Authority              *authzcontext.Capturer
	spawnAgents            func(context.Context, string) []string
	Sockets                *approvalstate.SocketCapabilityRuntime
	DirectIP               *approvalstate.DirectIPCapabilityRuntime
	Detections             *Detections
	Presence               *presence.Broker
	Unlocks                *presence.Unlocks
	Spans                  *secretspan.Screener
	publishVault           func(string, *presence.Unlocks)
	ctx                    context.Context
	database               *db.Store
	sessions               *store.SQL
	projects               *project.SQLRegistry
	trust                  *settings.TrustSurfacesStore
	remember               func(secretmatch.RememberFunc)
	sweep                  func(context.Context, string, uint64)
	Matcher                *secretmatch.Matcher
	Ignores                *projectignore.SecretService
	Harvest                *secretharvest.Runtime
	Capabilities           *secretcap.Service
	Fingerprinter          *secretmatch.Fingerprinter
	releaseCaptureRedactor func()
	releaseMessageRedactor func()
}

func New(ctx context.Context, database *db.Store, sessions *store.SQL, projects *project.SQLRegistry, trust *settings.TrustSurfacesStore) *Runtime {
	return &Runtime{ctx: ctx, database: database, sessions: sessions, projects: projects, trust: trust, Detections: &Detections{}}
}
func (b *Runtime) BindRemember(set func(secretmatch.RememberFunc)) {
	b.remember = set
	if set != nil && b.Harvest != nil {
		set(func(root string, values []secretmatch.Remembered) { b.Harvest.Remember(root, values...) })
	}
}
func secretScreenProject(ctx context.Context) string {
	if projectID := secretmatch.AskAttributionFrom(ctx).ProjectID; projectID != "" {
		return projectID
	}
	return curationctx.SessionFrom(ctx).ProjectID
}

func (b *Runtime) BindTrust(trust *settings.TrustSurfacesStore) { b.trust = trust }

// ReleaseRedactors detaches process screening callbacks owned by this runtime.
func (b *Runtime) ReleaseRedactors() {
	if b.releaseCaptureRedactor != nil {
		b.releaseCaptureRedactor()
		b.releaseCaptureRedactor = nil
	}
	if b.releaseMessageRedactor != nil {
		b.releaseMessageRedactor()
		b.releaseMessageRedactor = nil
	}
}
