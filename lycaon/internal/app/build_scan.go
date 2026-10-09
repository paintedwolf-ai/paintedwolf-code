package app

import (
	"context"

	"github.com/lycaon/lycaon/internal/projectcontrib"
	"github.com/lycaon/lycaon/internal/session"
	"github.com/lycaon/lycaon/pkg/api"
)

func (b *serveBuilder) wireScan() error {
	if _, err := b.security.LoadMatcher(b.startup.cfg.TestSecretMatcher); err != nil {
		return err
	}
	b.sessions.Manager.SetScanWaitState(session.ScanWaitState{
		InFlight: func(ctx context.Context, sessionID string) bool {
			requested, qErr := b.scanning.Store.ListBySessionID(ctx, sessionID)
			if qErr != nil {
				return true
			}
			for _, requestedScan := range requested {
				if requestedScan.Status == api.CodeScanStatusPending || requestedScan.Status == api.CodeScanStatusRunning {
					return true
				}
			}
			owed, oErr := b.scanning.Store.FullPassOwedForSession(ctx, sessionID)
			return oErr != nil || owed
		},
		Requested: func(ctx context.Context, sessionID, scanID string) bool {
			requested, qErr := b.scanning.Store.ListBySessionID(ctx, sessionID)
			if qErr != nil {
				return false
			}
			for _, requestedScan := range requested {
				if requestedScan.ID == scanID {
					return true
				}
			}
			return false
		},
	})
	b.sessions.Manager.SetScanGuidance(b.scanning.Guidance)
	if b.worker.merge != nil {
		b.worker.merge.Scans = b.scanning.Cadence
	}
	if err := b.scanning.RegisterTools(b.execution.Host.Registry, b.execution.Rejections, b.settings.Service); err != nil {
		return err
	}
	return b.workflows.RegisterTools(b.execution.Host.Registry, b.execution.Host.Boundary, b.storage.Sessions, b.settings.ProjectSurfaceGate(projectcontrib.SurfaceScanConfig, b.storage.Projects))
}
