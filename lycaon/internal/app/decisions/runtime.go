package decisions

import (
	"context"
	"fmt"
	"github.com/lycaon/lycaon/internal/coordinator/turnload"
	"github.com/lycaon/lycaon/internal/decide"
	"github.com/lycaon/lycaon/internal/decide/bialy"
	"log/slog"
)

type Runtime struct {
	Decider decide.Decider
	Rerank  decide.Reranker
}

// Load selects the configured decision engine and its ranking policies.
func (b *Runtime) Load(override decide.Decider) error {
	policies, err := decide.LoadPolicies()
	if err != nil {
		return fmt.Errorf("decision catalog: %w", err)
	}
	if override != nil {
		b.Decider = override
	} else {
		cfg := bialy.ConfigFromEnvironment()
		if catalog, err := turnload.LoadCatalog(); err == nil {
			cfg.HeadMaxLen = catalog.State.HeadTokens
		}
		b.Decider = bialy.New(cfg)
	}
	b.Rerank = decide.Reranker{Decider: b.Decider, Policies: policies}
	return nil
}

func (b *Runtime) Warmer() (interface{ Warm(context.Context) error }, bool) {
	if b.Decider == nil || !b.Decider.Available() {
		return nil, false
	}
	warmer, ok := b.Decider.(interface{ Warm(context.Context) error })
	return warmer, ok
}

// Warm prepares decision weights outside the startup path.
func (b *Runtime) Warm(ctx context.Context) error {
	warmer, ok := b.Warmer()
	if !ok {
		return nil
	}
	if client, ok := b.Decider.(*bialy.Client); ok {
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
