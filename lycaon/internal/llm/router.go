package llm

import (
	"context"
	"fmt"
	"strings"

	"github.com/lycaon/lycaon/internal/scopedstore"
	"github.com/lycaon/lycaon/pkg/api"
)

// StaticModelRouter resolves models from model_policy (coordinator, lite, agent pool).
type StaticModelRouter struct {
	policy      *PolicyStore
	pool        *PoolSelector
	pools       *scopedstore.LRU[*PoolSelector]
	scope       SettingsScope
	projectDir  string
	projectDirs []string
}

// NewStaticModelRouter constructs a router bound to the policy store.
func NewStaticModelRouter(policy *PolicyStore) *StaticModelRouter {
	p, _ := policy.Get(SettingsScopeGlobal, "")
	pools := scopedstore.New[*PoolSelector](scopedstore.DefaultEntries)
	pool := NewPoolSelector(p)
	pools.Store(string(SettingsScopeGlobal)+"\x00", pool)
	return &StaticModelRouter{
		policy: policy,
		pool:   pool,
		pools:  pools,
		scope:  SettingsScopeGlobal,
	}
}

// WithOverlayRoots binds the router to project overlay roots.
func (r *StaticModelRouter) WithOverlayRoots(projectDirs []string) *StaticModelRouter {
	clean := make([]string, 0, len(projectDirs))
	for _, projectDir := range projectDirs {
		if projectDir = strings.TrimSpace(projectDir); projectDir != "" {
			clean = append(clean, projectDir)
		}
	}
	if len(clean) == 0 {
		return r.WithScope(SettingsScopeGlobal, "")
	}
	cp := *r
	cp.scope = SettingsScopeProject
	cp.projectDirs = clean
	cp.projectDir = clean[len(clean)-1]
	p, _ := r.policy.GetForProjectRoots(clean)
	cp.pool = cp.scopedPool(p)
	return &cp
}

// WithScope sets scope for subsequent resolutions.
func (r *StaticModelRouter) WithScope(scope SettingsScope, projectDir string) *StaticModelRouter {
	cp := *r
	cp.scope = scope
	cp.projectDir = projectDir
	if scope == SettingsScopeProject && strings.TrimSpace(projectDir) != "" {
		cp.projectDirs = []string{projectDir}
	} else {
		cp.projectDirs = nil
	}
	p, _ := r.policy.Get(scope, projectDir)
	cp.pool = cp.scopedPool(p)
	return &cp
}

// Request rebinding shares the project scheduling cursor.
// Eviction resets inactive scopes; workers retain their assigned picks.
func (r *StaticModelRouter) scopedPool(policy ModelPolicy) *PoolSelector {
	roots := make([]string, 0, len(r.projectDirs))
	seen := make(map[string]bool, len(r.projectDirs))
	for _, root := range r.projectDirs {
		key := policyProjectKey(root)
		if !seen[key] {
			roots = append(roots, key)
			seen[key] = true
		}
	}
	key := string(r.scope) + "\x00" + strings.Join(roots, "\x00")
	pool, _ := r.pools.LoadOrStore(key, NewPoolSelector(policy))
	return pool
}

func (r *StaticModelRouter) effectivePolicy() (ModelPolicy, error) {
	if len(r.projectDirs) > 0 {
		return r.policy.GetForProjectRoots(r.projectDirs)
	}
	return r.policy.Get(r.scope, r.projectDir)
}

// Coordinator returns the coordinator model selection.
func (r *StaticModelRouter) Coordinator(ctx context.Context) (*ModelSelection, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	p, err := r.effectivePolicy()
	if err != nil {
		return nil, err
	}
	ref := p.Coordinator
	return selectionFromRef(ref, ModelRoleCoordinator, false), nil
}

// Lite returns the compact model selection.
func (r *StaticModelRouter) Lite(ctx context.Context) (*ModelSelection, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	p, err := r.effectivePolicy()
	if err != nil {
		return nil, err
	}
	ref := SummarizerRef(p)
	return selectionFromRef(ref, ModelRoleLite, false), nil
}

// SelectGroup picks n diverse models from the agent pool.
func (r *StaticModelRouter) SelectGroup(ctx context.Context, n int) ([]ModelSelection, error) {
	p, err := r.effectivePolicy()
	if err != nil {
		return nil, err
	}
	r.pool.UpdatePolicy(p)
	return r.pool.SelectGroup(ctx, n)
}

// Select picks one model from the agent pool for subagent work.
func (r *StaticModelRouter) Select(ctx context.Context) (*ModelSelection, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	p, err := r.effectivePolicy()
	if err != nil {
		return nil, err
	}
	r.pool.UpdatePolicy(p)
	return r.pool.SelectOne(ctx)
}

// ResolveSession returns session override, pool model for worker children, or coordinator default.
func (r *StaticModelRouter) ResolveSession(ctx context.Context, sess *api.Session) (*ModelSelection, error) {
	if sess == nil {
		return nil, fmt.Errorf("nil session")
	}
	if sess.ProviderID != "" && sess.Model != "" {
		return &ModelSelection{
			ProviderID: sess.ProviderID,
			Model:      sess.Model,
		}, nil
	}
	if strings.TrimSpace(sess.ParentSessionID) != "" {
		p, err := r.effectivePolicy()
		if err != nil {
			return nil, err
		}
		r.pool.UpdatePolicy(p)
		return r.pool.SelectOne(ctx)
	}
	return r.Coordinator(ctx)
}
