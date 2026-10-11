package configuration

import (
	"context"
	"fmt"

	"github.com/lycaon/lycaon/internal/confine"
	"github.com/lycaon/lycaon/internal/hostpower"
	"github.com/lycaon/lycaon/internal/hostresources"
	"github.com/lycaon/lycaon/internal/settings"
)

type Runtime struct {
	SessionLimits settings.SessionLimits
	Service       *settings.Service
	HostResources *hostresources.Service
	Power         *hostpower.Controller
}

func (b *Runtime) Load(cfg Config) error {
	var err error
	b.SessionLimits = settings.DefaultSessionLimits()
	if cfg.TestSessionLimits != nil {
		b.SessionLimits = settings.NormalizeSessionLimits(*cfg.TestSessionLimits)
	}

	b.Service, err = settings.NewService()
	if err != nil {
		return fmt.Errorf("settings service: %w", err)
	}
	if b.Service != nil && b.Service.Approvals != nil {
		// Confinement reads durable write-root grants live.
		confine.SetGrantedWriteRootsSource(b.Service.Approvals.WriteRootsForProject)
	}

	return nil
}

func (b *Runtime) BuildHostResources(dataDir string) error {
	service, err := hostresources.NewService(dataDir)
	if err != nil {
		return fmt.Errorf("host resources service: %w", err)
	}
	b.HostResources = service
	return nil
}

// A nil project surface gate is closed.
func (b *Runtime) ProjectSurfaceGate(surface string, projects settings.ProjectLookup) *settings.ProjectSurfaceGate {
	if b == nil || b.Service == nil || b.Service.TrustSurfaces == nil || projects == nil {
		return nil
	}
	return &settings.ProjectSurfaceGate{
		Surface:  surface,
		Surfaces: b.Service.TrustSurfaces,
		Projects: projects,
	}
}

func (b *Runtime) BuildHostPower(resources ResourceLifetime) {
	keepAwake := true
	if b.Service != nil && b.Service.Power != nil {
		keepAwake = b.Service.Power.KeepAwakeWhileWorking()
	}
	b.Power = hostpower.New(keepAwake)
	controller := b.Power
	resources.Track("host-power", 25, func(context.Context) error { return controller.Close() })
}

type ResourceLifetime interface {
	Track(string, int, func(context.Context) error)
}
