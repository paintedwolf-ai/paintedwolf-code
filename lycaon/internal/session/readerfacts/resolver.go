// Package readerfacts resolves the session host facts every prompt Reader for
// a session derives from.
package readerfacts

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/lycaon/lycaon/internal/gate"
	"github.com/lycaon/lycaon/internal/gitrepo"
	"github.com/lycaon/lycaon/internal/projectroot"
	"github.com/lycaon/lycaon/internal/prompts"
	"github.com/lycaon/lycaon/internal/sandbox"
	sessioncatalog "github.com/lycaon/lycaon/internal/session/catalog"
	"github.com/lycaon/lycaon/internal/session/profiles"
	sessionscope "github.com/lycaon/lycaon/internal/session/scope"
	"github.com/lycaon/lycaon/internal/session/toolcontext"
	"github.com/lycaon/lycaon/internal/session/workerworkspace"
	"github.com/lycaon/lycaon/internal/spawn"
	"github.com/lycaon/lycaon/internal/toolschema"
	"github.com/lycaon/lycaon/pkg/api"
)

// Sources are the session services host facts come from.
type Sources struct {
	Catalog     *sessioncatalog.Service
	Profiles    *profiles.Service
	Workspace   *sessionscope.Service
	Workspaces  *workerworkspace.Service
	ToolContext *toolcontext.Service
}

// Resolver derives prompts.HostFacts for a live session.
type Resolver struct {
	src        Sources
	webSearch  func() bool
	runContext func(ctx context.Context, sessionID string) (api.CoordinatorRunContext, error)
}

// New returns a Resolver over src; Bind completes it.
func New(src Sources) *Resolver { return &Resolver{src: src} }

// Bind installs the web-search predicate the turn projection also applies and
// the coordinator run context that declares the dispatch roster.
func (r *Resolver) Bind(webSearch func() bool, runContext func(context.Context, string) (api.CoordinatorRunContext, error)) {
	r.webSearch, r.runContext = webSearch, runContext
}

// Observation is what the host observes about one session; Derive turns it
// into the facts a Reader reads.
type Observation struct {
	Session *api.Session
	// Profiles are the session's catalog profiles constrained by its posture.
	Profiles []sandbox.ToolProfile
	Schemas  *toolschema.Config
	Roots    []projectroot.RootRef
	// BranchRoot is a worker's isolated checkout; OnWorkerBranch reports one.
	BranchRoot            string
	OnWorkerBranch        bool
	WebSearch             bool
	ApprovalPosture       gate.Posture
	CommandExternalHidden bool
	SessionScratchDir     string
	SharedTempLocations   []string
	// AllowedAgents is the run's declared roster; nil uses the ambient roster.
	AllowedAgents []string
	// IsRepo reports whether a path is inside a Git repository.
	IsRepo func(path string) bool
}

// Derive computes host facts from one observation.
func Derive(obs Observation) (prompts.HostFacts, error) {
	sess := obs.Session
	if sess == nil {
		return prompts.HostFacts{}, errors.New("reader facts: no session")
	}
	if len(obs.Profiles) == 0 {
		return prompts.HostFacts{}, fmt.Errorf("reader facts: no catalog tool profiles for session %s", sess.ID)
	}
	if obs.IsRepo == nil {
		return prompts.HostFacts{}, errors.New("reader facts: no repository probe")
	}
	hasGit, hasAnyGit := gitFacts(obs.Roots, sess, obs.BranchRoot, obs.IsRepo)
	mode := string(api.SessionModeAgent)
	if sess.Mode == api.SessionModeAsk {
		mode = string(api.SessionModeAsk)
	}
	facts := prompts.HostFacts{
		SessionMode:           mode,
		Profiles:              obs.Profiles,
		Schemas:               obs.Schemas,
		RootCount:             len(obs.Roots),
		HasGit:                hasGit,
		HasAnyGitRoot:         hasAnyGit,
		OnWorkerBranch:        obs.OnWorkerBranch,
		WebSearch:             obs.WebSearch,
		ReviewsConfinedWrites: obs.ApprovalPosture.ReviewsConfinedWrites(),
		CommandExternalHidden: obs.CommandExternalHidden,
		SessionScratchDir:     strings.TrimSpace(obs.SessionScratchDir),
		SharedTempLocations:   obs.SharedTempLocations,
	}
	if !sess.IsWorkerChild() {
		facts.SpawnAllowlist = append([]string(nil), obs.AllowedAgents...)
		if len(facts.SpawnAllowlist) == 0 {
			facts.SpawnAllowlist = spawn.AmbientAllowedAgents()
		}
	}
	return facts, nil
}

// HostFacts resolves sess's host facts, or an error naming the fact that could
// not be established.
func (r *Resolver) HostFacts(ctx context.Context, sess *api.Session) (prompts.HostFacts, error) {
	obs, err := r.Observe(ctx, sess)
	if err != nil {
		return prompts.HostFacts{}, err
	}
	return Derive(obs)
}

// Observe gathers sess's observation from the session services.
func (r *Resolver) Observe(ctx context.Context, sess *api.Session) (Observation, error) {
	if r == nil || sess == nil {
		return Observation{}, errors.New("reader facts: no session")
	}
	if r.webSearch == nil {
		return Observation{}, errors.New("reader facts: web search state unbound")
	}
	src := r.src
	if src.Catalog == nil || src.Profiles == nil || src.Workspace == nil || src.Workspaces == nil || src.ToolContext == nil {
		return Observation{}, errors.New("reader facts: session sources incomplete")
	}
	// Without a session view, admission reads the shipped profiles; so does the Reader.
	var (
		profiles []sandbox.ToolProfile
		schemas  *toolschema.Config
	)
	if view := src.Catalog.ViewForSession(ctx, sess); view != nil {
		profiles, schemas = view.ToolProfiles, view.ToolSchemas
	}
	if len(profiles) == 0 {
		shipped, err := sandbox.LoadToolProfiles()
		if err != nil {
			return Observation{}, fmt.Errorf("reader facts: tool profiles: %w", err)
		}
		profiles = shipped
	}
	roots, err := src.Workspace.Roots(ctx, sess)
	if err != nil {
		return Observation{}, fmt.Errorf("reader facts: workspace roots: %w", err)
	}
	branchRoot, onBranch := src.Workspaces.OnWorkerBranch(ctx, sess)
	obs := Observation{
		Session:               sess,
		Profiles:              src.Profiles.ConstrainSessionProfiles(ctx, sess, profiles),
		Schemas:               schemas,
		Roots:                 roots,
		BranchRoot:            branchRoot,
		OnWorkerBranch:        onBranch,
		WebSearch:             r.webSearch(),
		ApprovalPosture:       src.Profiles.ProjectApprovalPosture(ctx, sess),
		CommandExternalHidden: src.Profiles.CommandExternalHidden(),
		SessionScratchDir:     src.ToolContext.ScratchDir(ctx, sess),
		SharedTempLocations:   src.Profiles.WritableSharedTemp(ctx, sess),
		IsRepo:                isRepo,
	}
	if !sess.IsWorkerChild() {
		if obs.AllowedAgents, err = r.allowedAgents(ctx, sess); err != nil {
			return Observation{}, err
		}
	}
	return obs, nil
}

func (r *Resolver) allowedAgents(ctx context.Context, sess *api.Session) ([]string, error) {
	if r.runContext == nil {
		return nil, errors.New("reader facts: coordinator run context unbound")
	}
	run, err := r.runContext(ctx, sess.ID)
	if err != nil {
		return nil, fmt.Errorf("reader facts: run context: %w", err)
	}
	return run.AllowedAgents, nil
}

// gitFacts reports whether the active root, or a worker's branch checkout,
// is a Git repository, and whether any root is.
func gitFacts(roots []projectroot.RootRef, sess *api.Session, branchRoot string, isRepo func(string) bool) (hasGit, hasAnyGit bool) {
	primary := ""
	if root, err := projectroot.ActiveRoot(roots, sess.WorkspaceRootID); err == nil {
		primary = root.Path
	} else if root, err := projectroot.PrimaryRoot(roots); err == nil {
		primary = root.Path
	}
	if strings.TrimSpace(primary) == "" {
		primary = sess.WorkspacePath
	}
	probe := func(path string) bool { return strings.TrimSpace(path) != "" && isRepo(strings.TrimSpace(path)) }
	hasGit = probe(primary) || probe(branchRoot)
	hasAnyGit = hasGit
	for _, root := range roots {
		if hasAnyGit {
			break
		}
		hasAnyGit = probe(root.Path)
	}
	return hasGit, hasAnyGit
}

func isRepo(path string) bool {
	path = strings.TrimSpace(path)
	if path == "" {
		return false
	}
	_, ok := gitrepo.Discover(path)
	return ok
}
