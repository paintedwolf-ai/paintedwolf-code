package app

import (
	"context"
	"fmt"
	"github.com/lycaon/lycaon/internal/anchorcatalog"
	"github.com/lycaon/lycaon/internal/configlayout"
	"github.com/lycaon/lycaon/internal/coordinator/anchor"
	"github.com/lycaon/lycaon/internal/events"
	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/mcp/bindings"
	"github.com/lycaon/lycaon/internal/oar"
	"github.com/lycaon/lycaon/internal/toolfeedback"
	"github.com/lycaon/lycaon/pkg/api"
)

// wireOARBlockPlane loads OAR rules, enables catalog Anchors for active
// families, and attaches the Emit/Binding block plane.
func (b toolWiring) wireOARBlockPlane() error {
	if b.execution.Host == nil || b.mgr == nil {
		return fmt.Errorf("oar: tool runtime and session manager required")
	}
	if b.catalog.DeviceView == nil || b.catalog.DeviceView.Rules == nil {
		return fmt.Errorf("oar: device catalog view required")
	}
	schemaDir := configlayout.SchemasDir(b.catalog.ModuleRoot)
	if schemaDir == "" {
		return fmt.Errorf("oar: no %s/ tree beside config root %s — desktop bundles must stage it (scripts/den-build-bundle.sh)",
			configlayout.SchemasDirName, b.catalog.ModuleRoot)
	}
	if err := anchorcatalog.InstallBundled(); err != nil {
		return fmt.Errorf("oar: load anchor catalog: %w", err)
	}
	// [OAR-FACT-24]: validate the capability document before anything is loaded
	// against it, and refuse to start when it is invalid. Every load decision —
	// which anchors resolve, which profiles a rule may require — is made from
	// this document, so a wrong one is worse than none.
	if err := oar.InstallCapabilityBundled(schemaDir); err != nil {
		return fmt.Errorf("oar: %w", err)
	}
	matcher, err := b.security.LoadMatcher(b.startup.cfg.TestSecretMatcher)
	if err != nil {
		return fmt.Errorf("oar secretmatch detector: %w", err)
	}
	detectors := oar.HostDetectors(matcher)
	loader, err := oar.NewLoader(schemaDir)
	if err != nil {
		return fmt.Errorf("oar loader: %w", err)
	}
	loader.SetDetectors(detectors)
	rs := b.catalog.DeviceView.Rules
	if err := oar.ValidateHostRuleSet(rs); err != nil {
		return err
	}
	pipeline := oar.NewGuardPipeline(rs, loader, oar.NewCounterStore())
	pipeline.SetDetectors(detectors)
	pipeline.SetFactProvider("secret_matches", func(gc *oar.GuardContext) error {
		findings, err := (oar.SecretMatchDetector{Matcher: matcher}).Inspect(gc)
		if err != nil {
			return err
		}
		oar.ApplyFindings(gc, findings)
		return nil
	})
	pipeline.EnableAnchor(oar.AnchorToolHandler)
	pipeline.EnableAnchor(oar.AnchorToolRejected)
	pipeline.EnableAnchor(oar.AnchorToolPreInvoke)
	pipeline.EnableAnchor(oar.AnchorSessionPreInvoke)
	pipeline.EnableAnchor(oar.AnchorCoordinatorPreInvoke)
	pipeline.EnableAnchor(oar.AnchorCoordinatorPostTurn)
	pipeline.EnableAnchor(oar.AnchorCoordinatorCloseoutCheck)
	pipeline.EnableAnchor(oar.AnchorToolPost)
	pipeline.EnableAnchor(oar.AnchorCredentialAssignment)
	pipeline.EnableAnchor(oar.AnchorWorkerFinalize)
	pipeline.EnableAnchor(oar.AnchorWorkerReportCheck)
	pipeline.EnableAnchor(oar.AnchorContentInput)
	pipeline.EnableAnchor(oar.AnchorContentOutput)
	pipeline.EnableAnchor(oar.AnchorContentToolResult)

	mgr := b.mgr
	pipeline.SetRuleSetFor(func(ctx context.Context, sessionID string) *oar.RuleSet {
		view := mgr.Catalog().ViewForSessionID(ctx, sessionID)
		if view == nil {
			return nil
		}
		return view.Rules
	})
	anchor.SetAnchorsFor(func(ctx context.Context, sessionID string) *anchor.Registry {
		view := mgr.Catalog().ViewForSessionID(ctx, sessionID)
		if view == nil {
			return nil
		}
		return view.Anchors
	})

	renderer := oar.NewRenderer(b.execution.Rejections, nudgeFormatter{f: b.execution.Rejections})
	bp := &toolfeedback.BlockPlane{Pipeline: pipeline, Renderer: renderer}
	b.execution.Host.Executor.Rejections.SetBlockPlane(bp)
	b.mgr.SetOARPipeline(pipeline, renderer)

	pipeline.SetMCPBindingsFor(func(ctx context.Context, sessionID string) []bindings.Binding {
		view := mgr.Catalog().ViewForSessionID(ctx, sessionID)
		if view == nil {
			return nil
		}
		return view.Bindings
	})
	return nil
}

type oarHostEventPublisher struct {
	publisher *events.Publisher
}

func (p oarHostEventPublisher) Publish(ctx context.Context, event oar.OnFireEvent) error {
	if p.publisher == nil {
		return fmt.Errorf("OAR event publisher is unavailable")
	}
	return p.publisher.PublishOAR(ctx, event.SessionID, api.OAROnFireEvent{
		Rule: event.Rule, Anchor: event.Anchor, Effect: string(event.Effect),
	})
}

type nudgeFormatter struct {
	f *guidance.StaticRejectFormatter
}

func (n nudgeFormatter) FormatNudge(ctx context.Context, code string, data map[string]any) (string, error) {
	return guidance.FormatCoordinatorNudge(ctx, n.f, code, data)
}
