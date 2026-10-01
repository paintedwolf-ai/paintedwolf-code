// Package harnessfixture prepares deterministic work through application services.
package harnessfixture

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"path/filepath"
	"sort"
	"strings"

	"github.com/google/uuid"
	"github.com/lycaon/lycaon/internal/fseffect"
	"github.com/lycaon/lycaon/internal/project"
	"github.com/lycaon/lycaon/internal/session"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/worker"
	"github.com/lycaon/lycaon/internal/workspacebaseline"
	"github.com/lycaon/lycaon/pkg/api"
)

type Overlay struct {
	Script              *WorkerScript     `json:"script,omitempty" yaml:"script,omitempty"`
	Verify              string            `json:"verify,omitempty" yaml:"verify,omitempty"`
	VerificationVerdict string            `json:"verification_verdict,omitempty" yaml:"verification_verdict,omitempty"`
	ResultStatus        string            `json:"result_status,omitempty" yaml:"result_status,omitempty"`
	Label               string            `json:"label" yaml:"label"`
	Files               map[string]string `json:"files" yaml:"files"`
}

// WorkerPolicyScripted declares that every worker the coordinator can reach is
// fixture-driven: an unplanned dispatch settles as failed instead of reaching a model.
const WorkerPolicyScripted = "scripted"

type Setup struct {
	Policy           string            `json:"policy,omitempty" yaml:"policy,omitempty"`
	Overlays         []Overlay         `json:"overlays" yaml:"overlays"`
	Dispatches       []DispatchScript  `json:"dispatches,omitempty" yaml:"dispatches,omitempty"`
	IntegrationFiles map[string]string `json:"integration_files" yaml:"integration_files"`
}

// Scripted reports whether any prepared child or fresh dispatch is fixture-driven.
func (s Setup) Scripted() bool {
	if len(s.Dispatches) > 0 || s.Policy == WorkerPolicyScripted {
		return true
	}
	for _, overlay := range s.Overlays {
		if overlay.Script != nil {
			return true
		}
	}
	return false
}

type Request struct {
	SessionID string `json:"session_id"`
	Setup     Setup  `json:"setup"`
}

type PreparedOverlay struct {
	ResultStatus   string                  `json:"result_status"`
	Verification   *tools.SourceRunCapture `json:"verification,omitempty"`
	ChildSessionID string                  `json:"child_session_id"`
	JobID          string                  `json:"job_id"`
	Label          string                  `json:"label"`
	BaselineSHA256 string                  `json:"baseline_sha256"`
}

type Evidence struct {
	Overlays []PreparedOverlay `json:"overlays"`
}

func (s Setup) Validate() error {
	if s.Policy != "" && s.Policy != WorkerPolicyScripted {
		return fmt.Errorf("unknown worker policy %q", s.Policy)
	}
	if len(s.Overlays) > 4 || len(s.Dispatches) > 4 || (len(s.Overlays) == 0 && len(s.Dispatches) == 0 && s.Policy != WorkerPolicyScripted) {
		return fmt.Errorf("fixture requires one to four overlays or dispatches, or a scripted worker policy")
	}
	keys := map[string]bool{}
	for _, dispatch := range s.Dispatches {
		if err := dispatch.Validate(); err != nil {
			return err
		}
		if keys[dispatch.Key()] {
			return fmt.Errorf("dispatch scripts must declare distinct scopes")
		}
		keys[dispatch.Key()] = true
	}
	seen := map[string]bool{}
	for _, overlay := range s.Overlays {
		if overlay.ResultStatus != "" && overlay.ResultStatus != "complete" && overlay.ResultStatus != "partial" {
			return fmt.Errorf("prepared worker result must be complete or partial")
		}
		if (overlay.Verify == "") != (overlay.VerificationVerdict == "") || (overlay.VerificationVerdict != "" && overlay.VerificationVerdict != api.SourceVerdictPassed && overlay.VerificationVerdict != api.SourceVerdictFailed) {
			return fmt.Errorf("prepared verification requires its command and passed or failed verdict")
		}
		if strings.TrimSpace(overlay.Label) == "" || seen[overlay.Label] || len(overlay.Files) == 0 {
			return fmt.Errorf("overlay requires a unique label and file changes")
		}
		if overlay.Script != nil {
			if overlay.ResultStatus != "partial" {
				return fmt.Errorf("a worker script continues a partial return")
			}
			if err := overlay.Script.Validate(); err != nil {
				return err
			}
		}
		seen[overlay.Label] = true
		if err := validateFiles(overlay.Files); err != nil {
			return err
		}
	}
	return validateFiles(s.IntegrationFiles)
}

func validateFiles(files map[string]string) error {
	for name, body := range files {
		if !filepath.IsLocal(name) || filepath.ToSlash(filepath.Clean(name)) != name || len(body) > 65536 {
			return fmt.Errorf("fixture file must be a normalized relative path of at most 64 KiB: %q", name)
		}
		for _, part := range strings.Split(name, "/") {
			if strings.HasPrefix(part, ".") {
				return fmt.Errorf("fixture cannot change hidden paths: %q", name)
			}
		}
	}
	return nil
}

// Prepare completes deterministic runner jobs without invoking a model. The local
// poller cannot claim these jobs; their ordinary overlay lifecycle remains intact.
type VerifyWorker func(context.Context, *api.Session, *api.WorkerTask, string) (*tools.SourceRunCapture, error)

func Prepare(ctx context.Context, queue worker.WorkerQueue, sessions session.Store, parent *api.Session, root project.Root, setup Setup, verify VerifyWorker) (Evidence, error) {
	if err := setup.Validate(); err != nil {
		return Evidence{}, err
	}
	messages, err := sessions.GetMessages(ctx, parent.ID)
	if err != nil {
		return Evidence{}, err
	}
	if hasConversation(messages) {
		return Evidence{}, fmt.Errorf("fixture requires a session before its first prompt")
	}
	jobs, err := queue.List(ctx, parent.ProjectID)
	if err != nil {
		return Evidence{}, err
	}
	if len(jobs) != 0 {
		return Evidence{}, fmt.Errorf("fixture requires a fresh project without worker jobs")
	}
	evidence := Evidence{Overlays: []PreparedOverlay{}}
	for _, overlay := range setup.Overlays {
		prepared, err := prepareOverlay(ctx, queue, sessions, parent, root, overlay, verify)
		if err != nil {
			return evidence, err
		}
		evidence.Overlays = append(evidence.Overlays, prepared)
	}
	for i := 1; i < len(evidence.Overlays); i++ {
		if evidence.Overlays[i].BaselineSHA256 != evidence.Overlays[0].BaselineSHA256 {
			return evidence, fmt.Errorf("prepared overlays do not share an identical baseline")
		}
	}
	return evidence, replaceFiles(root.Path, setup.IntegrationFiles)
}

func prepareOverlay(ctx context.Context, queue worker.WorkerQueue, sessions session.Store, parent *api.Session, root project.Root, overlay Overlay, verify VerifyWorker) (PreparedOverlay, error) {
	id := uuid.NewString()
	paths := sortedPaths(overlay.Files)
	child, err := enqueueFixtureWorker(ctx, queue, sessions, parent, root, overlay, id, paths)
	if err != nil {
		return PreparedOverlay{}, err
	}
	claimed, err := queue.ClaimNext(ctx, worker.ClaimRequest{ProjectID: parent.ProjectID, ClaimedBy: "local", ExecutionTarget: api.ExecutionTargetRunner})
	if err != nil {
		return PreparedOverlay{}, err
	}
	if claimed.ID != id {
		return PreparedOverlay{}, fmt.Errorf("fixture claimed an unexpected worker")
	}
	branch, err := queue.ClaimWorkerBranch(ctx, id)
	if err != nil {
		return PreparedOverlay{}, err
	}
	if err := replaceFiles(branch.WorkspaceRoot, overlay.Files); err != nil {
		return PreparedOverlay{}, err
	}
	var validation *tools.SourceRunCapture
	if overlay.Verify != "" {
		if verify == nil {
			return PreparedOverlay{}, fmt.Errorf("fixture worker verification is not configured")
		}
		validation, err = verify(ctx, child, branch, overlay.Verify)
		if err != nil {
			return PreparedOverlay{}, err
		}
		if validation == nil || validation.Verdict != overlay.VerificationVerdict {
			return PreparedOverlay{}, fmt.Errorf("prepared worker did not reach its declared validation outcome")
		}
	}
	// Fixture results arrive with the initial prompt. Acknowledge their delivery
	// before settlement so the live poller cannot wake an unprompted coordinator.
	if err := queue.MarkOutcomeDelivered(ctx, id); err != nil {
		return PreparedOverlay{}, err
	}
	branch.ClaimToken = claimed.ClaimToken
	status := overlay.ResultStatus
	if status == "" {
		status = "complete"
	}
	completed, err := queue.Complete(ctx, branch, api.WorkerResult{
		Status: status, HostAssembled: true, Summary: overlay.Label,
		CompletionReport: &api.WorkerCompletionReport{LegStatus: status, FilesModified: paths,
			RemainingRisk: []string{"Changes have not been integrated."}},
	})
	if err != nil {
		return PreparedOverlay{}, err
	}
	if !completed {
		return PreparedOverlay{}, fmt.Errorf("fixture lost its worker claim")
	}
	settled, ok := queue.Get(id)
	if !ok || settled.Status != api.WorkerStatusComplete || settled.MergeStatus != api.WorkerMergeStatusPending || settled.Result == nil || settled.Result.Status != status || settled.Result.CompletionReport == nil || settled.Result.CompletionReport.LegStatus != status {
		return PreparedOverlay{}, fmt.Errorf("prepared worker did not retain its declared terminal result and pending overlay")
	}

	baseline, err := workspacebaseline.Open(ctx, branch.WorkspaceBaselinePath, workspacebaseline.ContentStore(branch.WorkspaceBaselinePath))
	if err != nil {
		return PreparedOverlay{}, err
	}
	defer func() { _ = baseline.Close() }()
	digest := sha256.New()
	err = baseline.Each(ctx, func(path string, file workspacebaseline.File) error {
		_, err := fmt.Fprintf(digest, "%q %d %q %t\n", path, file.Size, file.SHA256, file.Opaque)
		return err
	})
	if err != nil {
		return PreparedOverlay{}, err
	}

	return PreparedOverlay{JobID: id, ChildSessionID: child.ID, Label: overlay.Label, BaselineSHA256: hex.EncodeToString(digest.Sum(nil)), Verification: validation, ResultStatus: status}, nil
}

func sortedPaths(files map[string]string) []string {
	paths := make([]string, 0, len(files))
	for path := range files {
		paths = append(paths, path)
	}
	sort.Strings(paths)
	return paths
}

func replaceFiles(root string, files map[string]string) error {
	for _, name := range sortedPaths(files) {
		_, err := fseffect.Replace(fseffect.ReplaceRequest{Location: fseffect.Location{Root: root, Rel: name}, Source: strings.NewReader(files[name]), Mode: 0o600})
		if err != nil {
			return err
		}
	}
	return nil
}

func enqueueFixtureWorker(ctx context.Context, queue worker.WorkerQueue, sessions session.Store, parent *api.Session, root project.Root, overlay Overlay, id string, paths []string) (*api.Session, error) {
	_, err := queue.Enqueue(ctx, api.WorkerTask{
		ID: id, ProjectID: parent.ProjectID, ParentSessionID: parent.ID,
		WorkspaceRootID: root.ID, WorkspacePath: root.Path, AgentType: "implementer",
		ExecutionTarget: api.ExecutionTargetRunner, RunnerID: "local", Prompt: "Implement " + overlay.Label,
		Brief: overlay.Label, Scope: &api.TaskScope{Mode: api.TaskScopeModeWrite, Paths: paths},
	})
	if err != nil {
		return nil, err
	}
	child, err := sessions.CreateChild(ctx, parent, api.SpawnChildRequest{
		AgentType: "implementer", Prompt: "Implement " + overlay.Label, Files: paths, WorkerJobID: id,
	})
	if err != nil {
		return nil, err
	}
	if err := queue.SetChildSessionID(ctx, id, child.ID); err != nil {
		return nil, err
	}
	return child, nil
}
