package app

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/lycaon/lycaon/internal/db"
	"github.com/lycaon/lycaon/internal/secretharvest"
	"github.com/lycaon/lycaon/internal/secretmatch"
	"github.com/lycaon/lycaon/internal/tools"
)

// credentialFiles records credential-file exposure and harvests bytes that are
// neither managed nor model-authored.
type credentialFiles struct {
	harvest  *secretharvest.Runtime
	fp       *secretmatch.Fingerprinter
	queries  *db.Queries
	managed  func(projectID string) []secretmatch.Remembered
	exposure func(ctx context.Context, sessionID, path string) error
}

func (b sessionWiring) newCredentialFiles() *credentialFiles {
	return &credentialFiles{
		harvest: b.secretHarvest, fp: b.secretFingerprinter, queries: db.New(b.db),
		managed: func(projectID string) []secretmatch.Remembered {
			if b.secretCaps == nil {
				return nil
			}
			return b.secretCaps.DurableScreeningValues(projectID)
		},
		exposure: b.store.MarkSecretExposureOnRead,
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
			// Without authorship every binding is harvested, which asks.
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

// Authored skips values already held as evidence: the model copied those
// rather than produced them.
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
