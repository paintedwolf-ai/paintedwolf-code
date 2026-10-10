package composition

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/lycaon/lycaon/config"
	"github.com/lycaon/lycaon/internal/conditions"
	"github.com/lycaon/lycaon/internal/fseffect"
	"github.com/lycaon/lycaon/internal/orchestration"
	"github.com/lycaon/lycaon/internal/settingsoverlay"
	workflowdef "github.com/lycaon/lycaon/internal/workflow/definition"
	workflowdrafts "github.com/lycaon/lycaon/internal/workflow/drafts"
	workflowvalidation "github.com/lycaon/lycaon/internal/workflow/validation"
	"github.com/lycaon/lycaon/pkg/api"
)

// PersistNotConfirmedError is returned when confirm is false without HITL approval.
type PersistNotConfirmedError struct{}

func (e *PersistNotConfirmedError) Error() string { return "persist not confirmed" }

// PersistRequest promotes a session workflow to the project overlay.
type PersistRequest struct {
	SessionID      string
	ProjectDir     string
	WorkflowID     string
	Version        string
	Confirm        bool
	Trigger        string
	SessionPosture api.SessionPosture
	CreatedBy      workflowdrafts.Actor
}

// PersistResult is a successful persist outcome.
type PersistResult struct {
	Path    string
	Summary api.WorkflowSummary
}

// Persister validates and writes session drafts to <overlay>/workflows/.
type Persister struct {
	// ModuleRoot mirrors Composer.ModuleRoot: a checkout root for `rules:` paths.
	ModuleRoot   string
	SessionStore workflowdrafts.Store
	Registry     *conditions.ConditionRegistry
	Obligations  workflowvalidation.ObligationSpecs
	Agents       orchestration.AgentRegistry
	Policy       *ComposePolicy
}

// Persist validates and atomically writes a session workflow to the project overlay.
func (p *Persister) Persist(ctx context.Context, req PersistRequest) (*PersistResult, error) {
	if p == nil || p.SessionStore == nil {
		return nil, fmt.Errorf("persist not configured")
	}
	sessionID := strings.TrimSpace(req.SessionID)
	workflowID := strings.TrimSpace(req.WorkflowID)
	version := strings.TrimSpace(req.Version)
	if sessionID == "" {
		return nil, &ComposeValidationFailed{Errors: []api.ComposeValidationError{{
			Field: "session_id", Code: "session_id_required", Message: "session_id required",
		}}}
	}
	if workflowID == "" {
		return nil, &ComposeValidationFailed{Errors: []api.ComposeValidationError{{
			Field: "workflow_id", Code: "workflow_id_required", Message: "workflow_id required",
		}}}
	}
	if version == "" {
		return nil, &ComposeValidationFailed{Errors: []api.ComposeValidationError{{
			Field: "version", Code: "version_required", Message: "version required",
		}}}
	}
	if !persistIDPattern.MatchString(workflowID) {
		return nil, &ComposeValidationFailed{Errors: []api.ComposeValidationError{{
			Field:   "workflow_id",
			Code:    "invalid_workflow_id",
			Message: fmt.Sprintf("workflow id %q must match ^[a-z][a-z0-9-]{1,48}$", workflowID),
		}}}
	}
	projectDir := strings.TrimSpace(req.ProjectDir)
	if projectDir == "" {
		return nil, &ComposeValidationFailed{Errors: []api.ComposeValidationError{{
			Field: "project_dir", Code: "project_dir_required", Message: "session project_dir required",
		}}}
	}
	rec, err := p.SessionStore.Get(ctx, sessionID, workflowID, version)
	if err != nil {
		return nil, err
	}
	// Approval is asked only about a draft that exists.
	if err := p.checkApproval(req); err != nil {
		return nil, err
	}
	raw, err := workflowdef.ParseManifestYAML([]byte(rec.ManifestYAML))
	if err != nil {
		return nil, &ComposeValidationFailed{Errors: []api.ComposeValidationError{{
			Field: "manifest", Code: "parse_error", Message: err.Error(),
		}}}
	}
	if raw.ID != workflowID {
		return nil, &ComposeValidationFailed{Errors: []api.ComposeValidationError{{
			Field:   "workflow_id",
			Code:    "id_mismatch",
			Message: fmt.Sprintf("url workflow_id %q does not match manifest id %q", workflowID, raw.ID),
		}}}
	}
	if raw.Version != version {
		return nil, &ComposeValidationFailed{Errors: []api.ComposeValidationError{{
			Field:   "version",
			Code:    "version_mismatch",
			Message: fmt.Sprintf("request version %q does not match session manifest version %q", version, raw.Version),
		}}}
	}
	triggerOverride := strings.TrimSpace(req.Trigger)
	featureDir, err := ResolvePersistFeatureDir(workflowID)
	if err != nil {
		return nil, &ComposeValidationFailed{Errors: []api.ComposeValidationError{{
			Field: "workflow_id", Code: "invalid_workflow_id", Message: err.Error(),
		}}}
	}
	effective, errs := p.validateForProject(req, raw)
	if len(errs) > 0 {
		return nil, &ComposeValidationFailed{Errors: errs}
	}
	// Preserve the manifest trigger when omitted; empty triggers are undiscoverable.
	if triggerOverride != "" {
		effective.Trigger = triggerOverride
	}
	if trigger := strings.TrimSpace(effective.Trigger); trigger != "" {
		if collision := p.checkTriggerCollision(projectDir, workflowdef.ManifestKey(workflowID, version), trigger); collision != nil {
			return nil, collision
		}
	}
	yamlOut, err := workflowdef.MarshalManifestYAML(effective)
	if err != nil {
		return nil, err
	}
	overlayDir := filepath.Join(projectDir, settingsoverlay.DirName(), "workflows", featureDir)
	if err := os.MkdirAll(overlayDir, 0o750); err != nil {
		return nil, err
	}
	if _, err := fseffect.Replace(fseffect.ReplaceRequest{
		Location: fseffect.Location{Root: overlayDir, Rel: config.WorkflowManifestName},
		Source:   strings.NewReader(yamlOut),
		Mode:     0o600,
		DirMode:  0o750,
	}); err != nil {
		return nil, err
	}
	if err := p.SessionStore.Delete(ctx, sessionID, workflowID, version); err != nil && !errors.Is(err, workflowdrafts.ErrNotFound) {
		return nil, err
	}
	summary := effective.Summary()
	summary.Scope = api.WorkflowScopeProject
	return &PersistResult{
		Path:    ProjectWorkflowOverlayPath(featureDir),
		Summary: summary,
	}, nil
}

func (p *Persister) checkApproval(req PersistRequest) error {
	if !req.Confirm {
		return &PersistNotConfirmedError{}
	}
	return nil
}

func (p *Persister) validateForProject(req PersistRequest, raw workflowdef.Manifest) (workflowdef.Manifest, []api.ComposeValidationError) {
	if p.Registry == nil {
		return workflowdef.Manifest{}, []api.ComposeValidationError{{
			Field: "manifest", Code: "registry_unconfigured", Message: "condition registry required",
		}}
	}
	parentCatalog, err := p.parentCatalog(req.ProjectDir)
	if err != nil {
		return workflowdef.Manifest{}, []api.ComposeValidationError{{
			Field: "manifest", Code: "catalog_error", Message: err.Error(),
		}}
	}
	extendsRef := strings.TrimSpace(raw.Extends)
	effective, err := workflowdef.ResolveManifestChain(raw, parentCatalog)
	if err != nil {
		return workflowdef.Manifest{}, workflowvalidation.ExtendsChainErrors(err)
	}
	effective = workflowdef.FinalizeManifest(effective)
	var errs []api.ComposeValidationError
	errs = append(errs, p.validateAgents(effective.AllowedAgents)...)
	errs = append(errs, p.validateRulesPaths(req.ProjectDir, effective.Rules)...)
	errs = append(errs, workflowvalidation.ValidateComposeManifest(p.Registry, p.Obligations, effective)...)
	if p.Policy != nil {
		errs = append(errs, p.Policy.Apply(ComposePolicyInput{
			Raw:            raw,
			Effective:      effective,
			ExtendsRef:     extendsRef,
			SessionPosture: req.SessionPosture,
			PersistTier:    true,
		})...)
	}
	return effective, errs
}

func (p *Persister) parentCatalog(projectDir string) (map[string]workflowdef.Manifest, error) {
	return (&Composer{}).parentCatalog(projectDir)
}

func (p *Persister) validateAgents(allowed []string) []api.ComposeValidationError {
	c := &Composer{Agents: p.Agents}
	return c.validateAgents(allowed)
}

func (p *Persister) validateRulesPaths(projectDir string, rules []string) []api.ComposeValidationError {
	c := &Composer{ModuleRoot: p.ModuleRoot}
	return c.validateRulesPaths(projectDir, rules)
}

func (p *Persister) checkTriggerCollision(projectDir, excludeKey, trigger string) *ComposeValidationFailed {
	trigger = strings.TrimSpace(trigger)
	if trigger == "" {
		return nil
	}
	bindings, err := p.collectTriggers(projectDir)
	if err != nil {
		return &ComposeValidationFailed{Errors: []api.ComposeValidationError{{
			Field: "trigger", Code: "trigger_scan_error", Message: err.Error(),
		}}}
	}
	if workflowKey, ok := bindings[trigger]; ok && workflowKey != excludeKey {
		return &ComposeValidationFailed{Errors: []api.ComposeValidationError{{
			Field:   "trigger",
			Code:    "trigger_collision",
			Message: fmt.Sprintf("trigger %q already declared by %s", trigger, workflowKey),
		}}}
	}
	return nil
}

func (p *Persister) collectTriggers(projectDir string) (map[string]string, error) {
	out := map[string]string{}
	bundledRaw, _, err := workflowdef.LoadPackManifestsForCatalog(nil)
	if err != nil {
		return nil, err
	}
	for k, m := range bundledRaw {
		if tr := strings.TrimSpace(m.Trigger); tr != "" {
			out[tr] = k
		}
	}
	if strings.TrimSpace(projectDir) != "" {
		overlayDir := filepath.Join(projectDir, settingsoverlay.DirName(), "workflows")
		if info, statErr := os.Stat(overlayDir); statErr == nil && info.IsDir() {
			overlayRaw, err := workflowdef.LoadManifestsRaw(overlayDir)
			if err != nil {
				return nil, err
			}
			for k, m := range overlayRaw {
				if tr := strings.TrimSpace(m.Trigger); tr != "" {
					out[tr] = k
				}
			}
		} else if statErr != nil && !os.IsNotExist(statErr) {
			return nil, statErr
		}
	}
	return out, nil
}
