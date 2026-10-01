package delegation

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/lycaon/lycaon/config"
	"github.com/lycaon/lycaon/internal/grounding"
	"github.com/lycaon/lycaon/pkg/api"
)

// GroundingConfig controls delegation grounding behavior.
type GroundingConfig struct {
	PostTurn       GroundingModeConfig    `yaml:"post_turn"`
	Closeout       GroundingModeConfig    `yaml:"closeout"`
	Write          GroundingModeConfig    `yaml:"write"`
	Ambient        AmbientGroundingConfig `yaml:"ambient"`
	CircuitBreaker CircuitBreakerConfig   `yaml:"circuit_breaker"`
}

// AmbientGroundingConfig controls default Build worker-queue grounding.
type AmbientGroundingConfig struct {
	PostTurn GroundingModeConfig `yaml:"post_turn"`
}

// GroundingModeConfig is warn | block | off.
type GroundingModeConfig struct {
	Mode string `yaml:"mode"`
}

// CircuitBreakerConfig limits repeated ungrounded claims.
type CircuitBreakerConfig struct {
	MaxUngroundedWarnings    int    `yaml:"max_ungrounded_warnings"`
	MaxConsecutiveUngrounded int    `yaml:"max_consecutive_ungrounded"`
	EscalateMode             string `yaml:"escalate_mode"`
}

var (
	bundledGroundingOnce sync.Once
	bundledGrounding     GroundingConfig
	bundledGroundingErr  error
)

// DefaultGroundingConfig loads the bundled grounding configuration.
// Fail-closed: panics if the bundled file cannot be loaded.
func DefaultGroundingConfig() GroundingConfig {
	bundledGroundingOnce.Do(func() {
		bundledGrounding, bundledGroundingErr = LoadGroundingConfig()
	})
	if bundledGroundingErr != nil {
		panic(bundledGroundingErr)
	}
	return bundledGrounding
}

// LoadGroundingConfig reads grounding.yaml from path. Missing/invalid fails closed.
func LoadGroundingConfig() (GroundingConfig, error) {
	data, err := config.Read(config.Grounding)
	if err != nil {
		return GroundingConfig{}, fmt.Errorf("read grounding config: %w", err)
	}
	var cfg GroundingConfig
	if err := config.DecodeYAML(data, &cfg); err != nil {
		return GroundingConfig{}, fmt.Errorf("parse grounding config: %w", err)
	}
	if cfg.PostTurn.Mode == "" {
		return GroundingConfig{}, fmt.Errorf("grounding config: post_turn.mode required")
	}
	if cfg.Ambient.PostTurn.Mode == "" {
		cfg.Ambient.PostTurn.Mode = cfg.PostTurn.Mode
	}
	if cfg.Closeout.Mode == "" {
		return GroundingConfig{}, fmt.Errorf("grounding config: closeout.mode required")
	}
	if cfg.Write.Mode == "" {
		cfg.Write.Mode = cfg.PostTurn.Mode
	}
	if cfg.CircuitBreaker.MaxUngroundedWarnings <= 0 {
		return GroundingConfig{}, fmt.Errorf("grounding config: circuit_breaker.max_ungrounded_warnings must be positive")
	}
	if cfg.CircuitBreaker.MaxConsecutiveUngrounded <= 0 {
		return GroundingConfig{}, fmt.Errorf("grounding config: circuit_breaker.max_consecutive_ungrounded must be positive")
	}
	if cfg.CircuitBreaker.EscalateMode == "" {
		return GroundingConfig{}, fmt.Errorf("grounding config: circuit_breaker.escalate_mode required")
	}
	return cfg, nil
}

// WriteGroundingMode returns write.mode when set, otherwise post_turn.mode. It
// governs evidence grounding for record_finding writes.
func (c GroundingConfig) WriteGroundingMode() string {
	if mode := strings.TrimSpace(c.Write.Mode); mode != "" {
		return mode
	}
	return strings.TrimSpace(c.PostTurn.Mode)
}

// AmbientPostTurnMode returns ambient post_turn.mode, defaulting to post_turn.mode.
func (c GroundingConfig) AmbientPostTurnMode() string {
	if mode := strings.TrimSpace(c.Ambient.PostTurn.Mode); mode != "" {
		return mode
	}
	return strings.TrimSpace(c.PostTurn.Mode)
}

// GroundingVerdict is the outcome of correlating coordinator output with the job ledger.
type GroundingVerdict struct {
	OK     bool
	Code   string
	Reason string
	LegID  string
}

// WorkerSummaryTag is metadata for a parent worker summary (no prose).
type WorkerSummaryTag struct {
	DelegationID string
	LegID        string
	JobID        string
	MessageID    string
	TS           time.Time
}

// GroundingInput is information-asymmetric verifier input.
type GroundingInput struct {
	SessionID       string
	DelegationID    string
	Delegation      api.Delegation
	Legs            []api.Leg
	Jobs            []api.WorkerTask
	SummaryTags     []WorkerSummaryTag
	LastTurnTools   []string
	UngroundedState grounding.UngroundedCounter
}

// DelegationGroundingGate correlates coordinator turns with delegation + worker state.
type DelegationGroundingGate interface {
	CheckTurn(ctx context.Context, in GroundingInput) GroundingVerdict
	CheckCloseout(ctx context.Context, in GroundingInput) GroundingVerdict
}

// SimpleDelegationGroundingGate is a pure-Go ledger correlator.
type SimpleDelegationGroundingGate struct {
	cfg      GroundingConfig
	Criteria CriteriaChecker
}

func NewSimpleDelegationGroundingGate(cfg GroundingConfig) *SimpleDelegationGroundingGate {
	if cfg.PostTurn.Mode == "" {
		cfg = DefaultGroundingConfig()
	}
	return &SimpleDelegationGroundingGate{cfg: cfg}
}

// defaultCompletionCriteria is stamped on legs at dispatch when empty.
var defaultCompletionCriteria = []string{"job:complete", "summary:present"}

// CheckTurn carries the circuit-breaker escalation gate for delegation coordinator
// turns. Completion grounding is enforced at closeout via the ledger and
// completion criteria — see CheckCloseout — never by scanning coordinator prose.
func (g *SimpleDelegationGroundingGate) CheckTurn(_ context.Context, in GroundingInput) GroundingVerdict {
	if g.cfg.PostTurn.Mode == "off" {
		return GroundingVerdict{OK: true}
	}
	if in.UngroundedState.Escalated && g.cfg.CircuitBreaker.EscalateMode == "block" {
		return GroundingVerdict{
			OK:     false,
			Code:   "COORDINATOR_GROUNDING_ESCALATED",
			Reason: "repeated ungrounded completion claims",
		}
	}
	return GroundingVerdict{OK: true}
}

// CheckCloseout verifies ledger + summary tags + criteria before delegation done.
func (g *SimpleDelegationGroundingGate) CheckCloseout(ctx context.Context, in GroundingInput) GroundingVerdict {
	if g.cfg.Closeout.Mode == "off" {
		return GroundingVerdict{OK: true}
	}
	for _, leg := range in.Legs {
		if leg.Status != api.LegStatusComplete {
			continue
		}
		if leg.WorkerID == "" {
			return GroundingVerdict{
				OK:     false,
				Code:   "COORDINATOR_UNGROUNDED_CLAIM",
				Reason: "leg complete without worker_id",
				LegID:  leg.ID,
			}
		}
		job, ok := jobByID(in.Jobs, leg.WorkerID)
		if !ok || job.Status != api.WorkerStatusComplete {
			return GroundingVerdict{
				OK:     false,
				Code:   "COORDINATOR_UNGROUNDED_CLAIM",
				Reason: "worker job not complete on ledger",
				LegID:  leg.ID,
			}
		}
		if api.WorkerTaskOverlayOpen(&job) {
			return GroundingVerdict{
				OK:     false,
				Code:   "COORDINATOR_CRITERIA_UNMET",
				Reason: "worker overlay has not landed",
				LegID:  leg.ID,
			}
		}
		if !hasSummaryTag(in.SummaryTags, in.DelegationID, leg.ID) {
			return GroundingVerdict{
				OK:     false,
				Code:   "COORDINATOR_UNGROUNDED_CLAIM",
				Reason: "missing tagged worker_summary on parent session",
				LegID:  leg.ID,
			}
		}
		criteria := leg.CompletionCriteria
		if len(criteria) == 0 {
			criteria = defaultCompletionCriteria
		}
		if !criteriaSatisfied(ctx, g, criteria, leg, in) {
			return GroundingVerdict{
				OK:     false,
				Code:   "COORDINATOR_CRITERIA_UNMET",
				Reason: "leg completion criteria not satisfied",
				LegID:  leg.ID,
			}
		}
	}
	return GroundingVerdict{OK: true}
}

func containsTool(tools []string, name string) bool {
	for _, t := range tools {
		if t == name {
			return true
		}
	}
	return false
}

func jobByID(jobs []api.WorkerTask, id string) (api.WorkerTask, bool) {
	for _, j := range jobs {
		if j.ID == id {
			return j, true
		}
	}
	return api.WorkerTask{}, false
}

func hasSummaryTag(tags []WorkerSummaryTag, delegationID, legID string) bool {
	for _, t := range tags {
		if t.DelegationID == delegationID && t.LegID == legID {
			return true
		}
	}
	return false
}

func criteriaSatisfied(ctx context.Context, g *SimpleDelegationGroundingGate, criteria []string, leg api.Leg, in GroundingInput) bool {
	for _, c := range criteria {
		if !criterionMet(ctx, g, c, leg, in) {
			return false
		}
	}
	return true
}

func criterionMet(ctx context.Context, g *SimpleDelegationGroundingGate, criterion string, leg api.Leg, in GroundingInput) bool {
	switch {
	case criterion == "job:complete":
		job, ok := jobByID(in.Jobs, leg.WorkerID)
		return ok && job.Status == api.WorkerStatusComplete
	case criterion == "summary:present":
		return hasSummaryTag(in.SummaryTags, in.DelegationID, leg.ID)
	case strings.HasPrefix(criterion, "file:modified:"):
		path := strings.TrimPrefix(criterion, "file:modified:")
		if g != nil && g.Criteria != nil {
			ok, err := g.Criteria.FileModified(ctx, in.Delegation.WorkspacePath, in.Delegation.BaseHeadSHA, path)
			return err == nil && ok
		}
		return false
	case strings.HasPrefix(criterion, "test:pass:"):
		cmd := strings.TrimPrefix(criterion, "test:pass:")
		if g != nil && g.Criteria != nil {
			ok, err := g.Criteria.TestPass(ctx, in.DelegationID, leg.ID, cmd)
			return err == nil && ok
		}
		return false
	default:
		return true
	}
}

// ApplyCircuitBreaker updates counters after an ungrounded verdict.
func ApplyCircuitBreaker(state *grounding.UngroundedCounter, verdict GroundingVerdict, cfg GroundingConfig) {
	if verdict.OK {
		grounding.ResetUngroundedStreak(state)
		return
	}
	grounding.ApplyUngroundedWarning(
		state,
		cfg.CircuitBreaker.MaxUngroundedWarnings,
		cfg.CircuitBreaker.MaxConsecutiveUngrounded,
		cfg.CircuitBreaker.EscalateMode == "block",
	)
}

// ErrGroundingPending is returned when closeout fails grounding checks.
var ErrGroundingPending = groundingError("grounding_pending")

type groundingError string

func (e groundingError) Error() string { return string(e) }
