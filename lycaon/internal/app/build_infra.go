package app

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"time"

	"github.com/lycaon/lycaon/internal/agentpresence"
	"github.com/lycaon/lycaon/internal/bootrecovery"
	"github.com/lycaon/lycaon/internal/configdir"
	"github.com/lycaon/lycaon/internal/coordinator/turnload"
	"github.com/lycaon/lycaon/internal/decide"
	"github.com/lycaon/lycaon/internal/decide/bialy"
	"github.com/lycaon/lycaon/internal/eventoutbox"
	"github.com/lycaon/lycaon/internal/events"
	"github.com/lycaon/lycaon/internal/hostidentity"
	"github.com/lycaon/lycaon/internal/observability"
	"github.com/lycaon/lycaon/internal/orchestration"
	"github.com/lycaon/lycaon/internal/project"
	"github.com/lycaon/lycaon/internal/sandbox"
	"github.com/lycaon/lycaon/internal/session"
	"github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/internal/skills"
	"github.com/lycaon/lycaon/internal/sourcefeed"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/tsparse"
	"github.com/lycaon/lycaon/internal/worker"
	"github.com/lycaon/lycaon/pkg/api"
)

func (b *serveBuilder) wireToolRuntime() error {
	if err := b.wireDecider(); err != nil {
		return err
	}
	var err error
	b.turnLoads = turnload.NewLedger()
	// The session manager does not exist yet; the resolver reaches it at call time.
	resolve := func(ctx context.Context, tctx tools.ToolContext, need string, cards []turnload.ToolCard) turnload.RequestOutcome {
		return b.mgr.ResolveToolRequest(ctx, tctx, need, cards)
	}
	record := func(ctx context.Context, tctx tools.ToolContext, outcome turnload.RequestOutcome, result turnload.RequestToolsResult, elapsed time.Duration) {
		b.mgr.RecordToolRequest(ctx, tctx, outcome, result, elapsed)
	}
	lookup := func(ctx context.Context, tctx tools.ToolContext, need string, roster []skills.Skill) turnload.LookupOutcome {
		return b.mgr.LookupSkills(ctx, tctx, need, roster)
	}
	b.toolRuntime, err = loadToolRuntime(b.settings.Service, b.catalog.ModuleRoot, b.catalog.Effective, b.turnLoads, resolve, record, lookup, b.rerank)
	if err != nil {
		return fmt.Errorf("tool runtime: %w", err)
	}
	b.providers.BindCurator()
	b.toolRuntime.Survey.SetReadEvidenceLedger(b.storage.Sessions)
	toolWiring{b}.wireDetectionPacks()
	hintCfg, rejectFmt, err := loadStockHintRegistry()
	if err != nil {
		return fmt.Errorf("hint registry: %w", err)
	}
	b.hintCfg = hintCfg
	b.rejectFmt = rejectFmt
	if b.rejectFmt != nil {
		b.toolRuntime.Authority.ApplyGuidanceRejects(b.rejectFmt)
	}
	schemaCfg, err := loadToolSchemas()
	if err != nil {
		return fmt.Errorf("tool schemas: %w", err)
	}
	b.toolRuntime.Executor.Metadata.SetToolSchemas(schemaCfg)
	b.toolReg = tools.NewExecutorRegistry(b.toolRuntime.Executor, b.toolRuntime.Registry)
	return nil
}

func (b *serveBuilder) wireAgents() error {
	var err error
	b.agentRegistry = orchestration.NewMemoryAgentRegistry()
	if err := orchestration.LoadRequiredAgentRegistry(b.startup.ctx, b.agentRegistry); err != nil {
		return fmt.Errorf("agent registry: %w", err)
	}
	if err := orchestration.ValidateGateAgents(b.agentRegistry); err != nil {
		return fmt.Errorf("agent registry gates: %w", err)
	}
	b.toolProfiles, err = sandbox.LoadToolProfiles()
	if err != nil {
		return fmt.Errorf("tool profiles: %w", err)
	}
	if err := orchestration.ValidateAgentToolProfiles(b.agentRegistry, b.toolProfiles); err != nil {
		return fmt.Errorf("agent tool profiles: %w", err)
	}
	b.postureRegistry, err = session.LoadPostureRegistry()
	if err != nil {
		return fmt.Errorf("posture registry: %w", err)
	}
	return nil
}

// wireDecider resolves the local decision engine and the rerank policies it
// carries into every ranking site; the decision-engine-warm runner loads the
// weights off the boot path, and an unresolved engine leaves every decision
// abstaining and every ranking site lexical.
func (b *serveBuilder) wireDecider() error {
	policies, err := decide.LoadPolicies()
	if err != nil {
		return fmt.Errorf("decision catalog: %w", err)
	}
	if b.startup.cfg.TestDecider != nil {
		b.decider = b.startup.cfg.TestDecider
	} else {
		cfg := bialy.ConfigFromEnvironment()
		if catalog, err := turnload.LoadCatalog(); err == nil {
			cfg.HeadMaxLen = catalog.State.HeadTokens
		}
		b.decider = bialy.New(cfg)
	}
	b.rerank = decide.Reranker{Decider: b.decider, Policies: policies}
	return nil
}

// deciderWarmer returns the engine's warm-up when it has one to run.
func (b *serveBuilder) deciderWarmer() (interface{ Warm(context.Context) error }, bool) {
	if b.decider == nil || !b.decider.Available() {
		return nil, false
	}
	warmer, ok := b.decider.(interface{ Warm(context.Context) error })
	return warmer, ok
}

// warmDecider completes the engine handshake once so the first decision does
// not pay for weight loading; a failure is logged and the engine stays
// available for a later attempt.
func (b *serveBuilder) warmDecider(ctx context.Context) error {
	warmer, ok := b.deciderWarmer()
	if !ok {
		return nil
	}
	if client, ok := b.decider.(*bialy.Client); ok {
		// Packaged apps bundle the checkpoint; a missing one is provisioned here.
		switch client.Status() {
		case bialy.ReasonDisabled, bialy.ReasonBinaryMissing:
			return nil
		case bialy.ReasonModelMissing:
			cfg := client.Config()
			slog.InfoContext(ctx, "decision checkpoint not installed; provisioning", "model", cfg.ModelID, "dir", cfg.ModelDir)
			if err := bialy.EnsureModel(ctx, cfg.ModelDir, nil); err != nil {
				if ctx.Err() == nil {
					slog.WarnContext(ctx, "decision checkpoint not provisioned; decisions abstain and ranking sites stay lexical", "error", err)
				}
				return nil
			}
		default:
		}
	}
	if err := warmer.Warm(ctx); err != nil && ctx.Err() == nil {
		slog.WarnContext(ctx, "decision engine did not warm", "error", err)
	}
	return nil
}

func (b *serveBuilder) wireEvents() error {
	var err error
	b.hub = events.WrapDebugHub(events.NewMemoryHub())
	b.eventOutbox = eventoutbox.New(b.storage.Database, b.hub)
	// An absent outbox would silently drop mutation events.
	if b.eventOutbox == nil {
		return fmt.Errorf("event outbox: nil after construction; every store wired below would drop its events")
	}
	b.storage.Sessions.SetEventOutbox(b.eventOutbox)
	b.storage.Projects.SetEventOutbox(b.eventOutbox)
	b.presence = events.NewPresence(b.hub, events.DefaultUserActionWindow)
	b.settings.BuildHostPower(b.startup.resources)
	eventLookup := project.ScopeLookup{Registry: b.storage.Projects}
	b.eventPub = &events.Publisher{
		Hub:               b.hub,
		Lookup:            eventLookup,
		Untrusted:         b.storage.Sessions,
		UserTurns:         b.storage.Sessions,
		ActivityObserver:  b.settings.Power,
		TurnClockObserver: b.settings.Power,
		SessionProject: func(ctx context.Context, sessionID string) (string, bool) {
			if b.storage.Sessions == nil {
				return "", false
			}
			sess, err := b.storage.Sessions.Get(ctx, sessionID)
			if err != nil || sess == nil || strings.TrimSpace(sess.ProjectID) == "" {
				return "", false
			}
			return sess.ProjectID, true
		},
		SessionLister: events.FuncSessionLister(func(ctx context.Context, projectID string) ([]string, error) {
			if b.mgr == nil {
				return nil, nil
			}
			page, err := b.mgr.ListProjectSessions(ctx, store.SummaryQuery{ProjectID: projectID, Limit: boardRepublishSessionLimit})
			if err != nil {
				return nil, err
			}
			ids := make([]string, 0, len(page.Sessions))
			for _, summary := range page.Sessions {
				ids = append(ids, summary.ID)
			}
			return ids, nil
		}),
		SessionRoots: events.FuncSessionRoots(func(ctx context.Context, sessionID string) (string, bool) {
			if b.storage.Sessions == nil {
				return "", false
			}
			sess, err := b.storage.Sessions.Get(ctx, sessionID)
			if err != nil || sess == nil {
				return "", false
			}
			path := strings.TrimSpace(sess.WorkspacePath)
			if path == "" {
				return "", false
			}
			return path, true
		}),
	}
	// Chats follow their turns from session events; documents arrive with the server.
	b.agentPresence = agentpresence.New(&presenceChats{store: b.storage.Sessions, workerJobs: func(ctx context.Context, childSessionID string) (*api.WorkerTask, bool) {
		if b.workerQueue == nil {
			return nil, false
		}
		return b.workerQueue.GetLatestByChildSessionID(ctx, childSessionID)
	}}, b.eventPub)
	b.eventPub.SessionObserver = b.agentPresence
	b.mgr.SetAgentPresence(b.agentPresence)
	// One revision counter across the direct and outbox session-event paths.
	b.storage.Sessions.SetSessionRevisions(b.eventPub)
	// Both paths read prompt_pending from the manager.
	b.storage.Sessions.SetPromptPending(b.mgr)
	b.eventPub.SessionState = b.mgr
	b.eventOutbox.OnDelivered = func(ctx context.Context, delivered eventoutbox.DeliveredEvent) {
		if err := b.settings.Power.ObserveDelivered(delivered.Topic, delivered.Data); err != nil {
			slog.WarnContext(ctx, "observe host power activity", "topic", delivered.Topic, "error", err)
		}
		if err := b.agentPresence.ObserveDelivered(ctx, delivered.Topic, delivered.Data); err != nil {
			slog.WarnContext(ctx, "observe agent presence", "topic", delivered.Topic, "error", err)
		}
		if events.AttentionLifecycleTopic(delivered.Topic) {
			b.eventPub.PublishAttention(ctx)
		}
	}
	b.eventOutbox.Start(b.startup.ctx)
	if err := (delegationWiring{b}).registerRecovery(bootrecovery.Entry{
		Name: "rewind-operations", Kind: bootrecovery.KindJournal, Phase: bootrecovery.PhaseBuild,
		After: []string{"tool-invocations", "source-mutations", "editor-documents"},
		Run:   b.mgr.RecoverRewinds,
	}); err != nil {
		return err
	}
	b.sourceFeedUnbinds = append(b.sourceFeedUnbinds,
		sourcefeed.Bind(outboxSourceFeed{outbox: b.eventOutbox, lookup: eventLookup}))

	if err := (sessionWiring{b}).wireCheckpointRuntime(); err != nil {
		return err
	}

	project.SetDefaultOpenPolicy(project.DefaultOpenPolicy())

	b.apiToken, b.tokenGenerated, err = resolveServeAPIToken()
	if err != nil {
		return fmt.Errorf("api token: %w", err)
	}
	configDir, err := configdir.UserConfigDir()
	if err != nil {
		return fmt.Errorf("config dir: %w", err)
	}
	if b.hostIdentity, err = hostidentity.LoadOrCreate(configDir); err != nil {
		return fmt.Errorf("host identity: %w", err)
	}

	logFields := []any{"level", os.Getenv("LYCAON_LOG_LEVEL")}
	if path := observability.ActiveLogFilePath(); path != "" {
		logFields = append(logFields, "file", path)
	}
	b.startup.logger.Info("logging configured", logFields...)
	b.workersCfg, err = worker.LoadWorkersConfig()
	if err != nil {
		return fmt.Errorf("workers config: %w", err)
	}
	return nil
}

func (b *serveBuilder) loadConfig() error {
	if _, err := tsparse.LoadConfig(); err != nil {
		return fmt.Errorf("source parsing: %w", err)
	}
	if err := b.settings.Load(b.startup.cfg); err != nil {
		return err
	}
	return b.catalog.Load(b.startup.ctx, b.startup.logger)
}
