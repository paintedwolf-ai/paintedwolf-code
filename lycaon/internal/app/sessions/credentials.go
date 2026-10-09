package sessions

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/lycaon/lycaon/internal/db"
	"github.com/lycaon/lycaon/internal/secretcap"
	"github.com/lycaon/lycaon/internal/secretharvest"
	"github.com/lycaon/lycaon/internal/secretmatch"
	"github.com/lycaon/lycaon/internal/secretmint"
	"github.com/lycaon/lycaon/internal/session"
	"github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/pkg/api"
)

type credentialFiles struct {
	harvest  *secretharvest.Runtime
	fp       *secretmatch.Fingerprinter
	queries  *db.Queries
	managed  func(projectID string) []secretmatch.Remembered
	exposure func(ctx context.Context, sessionID, path string) error
}

func newCredentialFiles(harvest *secretharvest.Runtime, fp *secretmatch.Fingerprinter, database *db.Store, capabilities *secretcap.Service, sessions *store.SQL) *credentialFiles {
	return &credentialFiles{
		harvest: harvest, fp: fp, queries: db.New(database),
		managed: func(projectID string) []secretmatch.Remembered {
			if capabilities == nil {
				return nil
			}
			return capabilities.DurableScreeningValues(projectID)
		},
		exposure: sessions.MarkSecretExposureOnRead,
	}
}

func (c *credentialFiles) Delivered(ctx context.Context, read tools.CredentialFileRead) error {
	if c == nil || c.exposure == nil {
		return errors.New("secret exposure recorder unavailable")
	}
	if err := c.exposure(ctx, read.SessionID, read.Container); err != nil {
		return err
	}
	if c.harvest == nil || c.fp == nil {
		return nil
	}
	held := c.managedFingerprints(read.ProjectID)
	if read.ProjectID != "" && read.RootID != "" && read.Path != "" {
		authored, err := c.queries.ListCredentialAuthoredValues(ctx, db.ListCredentialAuthoredValuesParams{
			ProjectID: read.ProjectID, RootID: read.RootID, Path: read.Path,
		})
		if err != nil {
			slog.WarnContext(ctx, "credential authorship unavailable",
				"component", "secret_harvest", "error", err)
		}
		for _, fp := range authored {
			held[secretmatch.SecretFingerprint(fp)] = true
		}
	}
	c.harvest.Harvest(secretharvest.ContainerRead{
		RootSessionID: read.RootSessionID, ProjectID: read.ProjectID,
		Container: read.Container, Content: []byte(read.Content),
		Held: func(fp secretmatch.SecretFingerprint) bool { return held[fp] },
	})
	return nil
}

func (c *credentialFiles) Authored(ctx context.Context, written tools.AuthoredCredentialValues) error {
	if c == nil || c.fp == nil || c.queries == nil {
		return nil
	}
	managed := c.managedFingerprints(written.ProjectID)
	now := db.FormatTime(time.Now())
	for _, value := range written.Values {
		fp := c.fp.Fingerprint(value)
		if managed[fp] || c.harvest.Has(written.RootSessionID, fp) {
			continue
		}
		if err := c.queries.InsertCredentialAuthoredValue(ctx, db.InsertCredentialAuthoredValueParams{
			ProjectID: written.ProjectID, RootID: written.RootID, Path: written.Path,
			ValueFingerprint: string(fp), SessionID: written.SessionID,
			ToolCallID: written.ToolCallID, CreatedAt: now,
		}); err != nil {
			return err
		}
	}
	return nil
}

func (c *credentialFiles) managedFingerprints(projectID string) map[secretmatch.SecretFingerprint]bool {
	held := map[secretmatch.SecretFingerprint]bool{}
	if c.managed == nil || projectID == "" {
		return held
	}
	for _, value := range c.managed(projectID) {
		held[c.fp.Fingerprint(value.Secret)] = true
	}
	return held
}

func wireCredentialObservations(mgr *session.Manager, fp *secretmatch.Fingerprinter, matcher *secretmatch.Matcher, harvest *secretharvest.Runtime) {
	if mgr == nil {
		return
	}
	mgr.SetCredentialSlotProvider(func(ctx context.Context, sess *api.Session) *secretmint.Inspector {
		if view := mgr.Catalog().ViewForSession(ctx, sess); view != nil {
			return view.CredentialSlots
		}
		return nil
	})
	mgr.SetSecretFingerprinter(fp)
	mgr.SetIgnoredCredentialCandidate(func(ctx context.Context, projectID, value string) bool {
		return matcher.Ignored(secretmatch.WithAskAttribution(ctx, secretmatch.AskAttribution{ProjectID: projectID}), value)
	})
	if harvest != nil {
		mgr.SetHarvestedFingerprint(func(root string, f secretmatch.SecretFingerprint) bool {
			return harvest.Has(root, f)
		})
	}
}
