package conditions

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"

	"github.com/lycaon/lycaon/pkg/api"
)

// ErrUnknownCondition is returned when Evaluate is called for an unregistered id.
var ErrUnknownCondition = errors.New("unknown condition")

// ErrDuplicateCondition is returned when Register is called twice for the same name.
var ErrDuplicateCondition = errors.New("duplicate condition")

// ConditionFunc evaluates one vocabulary leaf.
type ConditionFunc func(ctx EvalContext) (bool, error)

// EvalContext is shared input for rule and manifest gate evaluation.
type EvalContext struct {
	Ctx                 context.Context
	SessionID           string
	SessionPosture      api.SessionPosture
	ProjectDir          string
	ProjectID           string
	ProjectRootCount    int
	BlueprintPath       string
	PlanContent         string
	WorkflowRunID       string
	WorkflowID          string
	Phase               string
	ReviewLoopActive    bool
	RunStatus           api.WorkflowRunStatus
	ToolName            string
	ToolArgs            map[string]any
	AllowedAgents       []string
	Vars                map[string]any
	ConditionID         string
	BindTopologyStage   string
	BindParallelGroup   []string
	EvidenceLegID       string
	EvidenceWorkspaceID string
}

// ConditionRegistry maps vocabulary ids to evaluators. Unknown ids fail closed.
type ConditionRegistry struct {
	mu            sync.RWMutex
	conditions    map[string]ConditionFunc
	applicability map[string]ConditionFunc
	paramPrefix   []paramEntry
}

type paramEntry struct {
	prefix string
	fn     ConditionFunc
}

// NewRegistry creates an empty registry.
func NewRegistry() *ConditionRegistry {
	return &ConditionRegistry{
		conditions:    map[string]ConditionFunc{},
		applicability: map[string]ConditionFunc{},
	}
}

// Register adds a condition evaluator. Duplicate names return ErrDuplicateCondition.
// Forbidden vocabulary ids return ErrForbiddenCondition.
func (r *ConditionRegistry) Register(name string, fn ConditionFunc) error {
	return r.register(name, fn, nil)
}

// RegisterEventScoped adds a condition with explicit applicability.
func (r *ConditionRegistry) RegisterEventScoped(name string, fn, active ConditionFunc) error {
	if active == nil {
		return fmt.Errorf("nil condition activity")
	}
	return r.register(name, fn, active)
}

func (r *ConditionRegistry) register(name string, fn, active ConditionFunc) error {
	if r == nil {
		return fmt.Errorf("nil registry")
	}
	name = strings.TrimSpace(name)
	if name == "" {
		return fmt.Errorf("empty condition name")
	}
	if err := checkForbiddenRegister(name); err != nil {
		return err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.conditions[name]; ok {
		return fmt.Errorf("%w: %s", ErrDuplicateCondition, name)
	}
	r.conditions[name] = fn
	if active != nil {
		r.applicability[name] = active
	}
	return nil
}

// RegisterParameterized registers fn for any id with the given prefix (e.g. evidence_passed:).
func (r *ConditionRegistry) RegisterParameterized(prefix string, fn ConditionFunc) error {
	if r == nil {
		return fmt.Errorf("nil registry")
	}
	prefix = strings.TrimSpace(prefix)
	if prefix == "" {
		return fmt.Errorf("empty parameterized prefix")
	}
	if err := checkForbiddenRegister(prefix); err != nil {
		return err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, e := range r.paramPrefix {
		if e.prefix == prefix {
			return fmt.Errorf("%w: %s", ErrDuplicateCondition, prefix)
		}
	}
	r.paramPrefix = append(r.paramPrefix, paramEntry{prefix: prefix, fn: fn})
	return nil
}

// Has reports whether name is registered (exact or parameterized prefix).
func (r *ConditionRegistry) Has(name string) bool {
	if r == nil {
		return false
	}
	name = strings.TrimSpace(name)
	r.mu.RLock()
	defer r.mu.RUnlock()
	if _, ok := r.conditions[name]; ok {
		return true
	}
	for _, e := range r.paramPrefix {
		if strings.HasPrefix(name, e.prefix) && len(name) > len(e.prefix) {
			return true
		}
	}
	return false
}

// Evaluate runs a registered condition. Unknown ids return (false, ErrUnknownCondition).
func (r *ConditionRegistry) Evaluate(name string, ctx EvalContext) (bool, error) {
	if r == nil {
		return false, ErrUnknownCondition
	}
	// Evaluators always receive a usable context.
	if ctx.Ctx == nil {
		ctx.Ctx = context.Background()
	}
	name = strings.TrimSpace(name)
	if IsForbidden(name) {
		return false, fmt.Errorf("%w: %s", ErrForbiddenCondition, name)
	}
	r.mu.RLock()
	fn, ok := r.conditions[name]
	if !ok {
		for _, e := range r.paramPrefix {
			if strings.HasPrefix(name, e.prefix) && len(name) > len(e.prefix) {
				fn, ok = e.fn, true
				break
			}
		}
	}
	r.mu.RUnlock()
	if !ok {
		return false, fmt.Errorf("%w: %s", ErrUnknownCondition, name)
	}
	ctx.ConditionID = name
	return fn(ctx)
}

// Applicable reports whether a registered condition is actionable.
func (r *ConditionRegistry) Applicable(name string, ctx EvalContext) (bool, error) {
	if r == nil {
		return false, ErrUnknownCondition
	}
	if ctx.Ctx == nil {
		ctx.Ctx = context.Background()
	}
	name = strings.TrimSpace(name)
	if IsForbidden(name) {
		return false, fmt.Errorf("%w: %s", ErrForbiddenCondition, name)
	}
	r.mu.RLock()
	_, registered := r.conditions[name]
	active := r.applicability[name]
	if !registered {
		for _, entry := range r.paramPrefix {
			if strings.HasPrefix(name, entry.prefix) && len(name) > len(entry.prefix) {
				registered = true
				break
			}
		}
	}
	r.mu.RUnlock()
	if !registered {
		return false, fmt.Errorf("%w: %s", ErrUnknownCondition, name)
	}
	if active == nil {
		return true, nil
	}
	ctx.ConditionID = name
	return active(ctx)
}

// ExactNames returns registered exact condition ids (not parameterized prefixes), sorted.
func (r *ConditionRegistry) ExactNames() []string {
	if r == nil {
		return nil
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]string, 0, len(r.conditions))
	for name := range r.conditions {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

// ParameterizedPrefixes returns registered parameterized prefixes, sorted.
func (r *ConditionRegistry) ParameterizedPrefixes() []string {
	if r == nil {
		return nil
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]string, 0, len(r.paramPrefix))
	for _, e := range r.paramPrefix {
		out = append(out, e.prefix)
	}
	sort.Strings(out)
	return out
}

// IsUnknownCondition reports registry lookup failures.
func IsUnknownCondition(err error) bool {
	return errors.Is(err, ErrUnknownCondition)
}

// EvalContextFromRun builds gate evaluation context for the current workflow phase.
func EvalContextFromRun(ctx context.Context, sess *api.Session, run *api.WorkflowRun, vars map[string]any) EvalContext {
	ec := EvalContext{Ctx: ctx, Vars: vars}
	if run != nil {
		ec.WorkflowRunID = run.ID
		ec.WorkflowID = run.WorkflowID
		ec.Phase = run.CurrentPhase
		ec.BlueprintPath = run.BlueprintPath
		ec.SessionID = run.SessionID
		ec.RunStatus = run.Status
	}
	if sess != nil {
		ec.SessionID = sess.ID
		ec.SessionPosture = sess.Posture
		ec.ProjectID = sess.ProjectID
		ec.ProjectDir = sess.WorkspacePath
	}
	return ec
}
