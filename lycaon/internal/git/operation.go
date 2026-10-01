package git

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"reflect"
	"strings"
	"time"

	"github.com/lycaon/lycaon/internal/gitexec"
	"github.com/lycaon/lycaon/internal/gitlease"
	"github.com/lycaon/lycaon/internal/gitrepo"
	"github.com/lycaon/lycaon/internal/gitstate"
	"github.com/lycaon/lycaon/internal/repochange"
)

// OperationRequest describes a local repository effect, never arbitrary argv.
type OperationRequest struct {
	Kind             string
	Action           string
	Branch           string
	Create           bool
	Ref              string
	Mode             string
	Message          string
	Paths            []string
	IncludeUntracked bool
	ReinstateIndex   bool
	// NoCommit stops a started merge before its commit, leaving it active.
	NoCommit bool
	Review   func(context.Context, []RestoreFile) error
}

// RepositoryState is observed even when Git reports a failed or partial effect.
type RepositoryState struct {
	Branch    string   `json:"branch"`
	Head      string   `json:"head"`
	Index     string   `json:"index"`
	MergeHead string   `json:"merge_head,omitempty"`
	Conflicts []string `json:"conflicts"`
	Dirty     bool     `json:"dirty"`
	Stash     string   `json:"stash_oid,omitempty"`
	StashLog  string   `json:"stash_log,omitempty"`
}

// OperationResult separates rehearsal refusals from attempted workspace effects.
type OperationResult struct {
	Available     bool            `json:"available"`
	Status        string          `json:"status"`
	Attempted     bool            `json:"attempted"`
	ExitCode      int             `json:"exit_code"`
	Before        RepositoryState `json:"before"`
	After         RepositoryState `json:"after"`
	AfterObserved bool            `json:"after_observed"`
	Paths         []string        `json:"reviewed_paths"`
	Diagnostics   string          `json:"diagnostics,omitempty"`
}

// OperationError is a structured precondition, not a classification of Git stderr.
type OperationError struct {
	Reason string
	Paths  []string
}

func (e *OperationError) Error() string { return "Git operation precondition: " + e.Reason }
func (e *OperationError) Code() string  { return "GIT_OPERATION_PRECONDITION" }

func operationRefusal(reason string, paths ...string) error {
	return &OperationError{Reason: reason, Paths: paths}
}

// Operate releases the lease for review, then revalidates before applying the effect.
func (m *Manager) Operate(ctx context.Context, root string, req OperationRequest) (OperationResult, error) {
	result := OperationResult{Paths: []string{}}
	repo, ok := gitrepo.Discover(root)
	if !ok {
		return result, ErrNotRepository
	}
	if repo.Root != gitrepo.CanonicalDir(root) {
		return result, operationRefusal("repository_root_required")
	}
	release, err := gitlease.Repository(ctx, repo.Root)
	if err != nil {
		return result, err
	}
	prepared, err := m.prepareOperation(ctx, repo, req)
	release()
	if err != nil {
		return result, err
	}
	defer prepared.close()
	result = prepared.result
	if result.Status == "failed" {
		return result, nil
	}
	if req.Review != nil {
		if err := req.Review(ctx, prepared.files); err != nil {
			return result, err
		}
	}
	return prepared.apply(ctx, repo)
}

func (p *preparedOperation) apply(ctx context.Context, repo gitrepo.Repo) (result OperationResult, err error) {
	result = p.result
	release, err := gitlease.Repository(ctx, repo.Root)
	if err != nil {
		return result, err
	}
	defer release()
	if err := p.validate(ctx, repo); err != nil {
		return result, err
	}
	// Re-resolve branch and stash selectors after review, including a branch that
	// was absent when creating it. Metadata snapshots alone do not cover packed refs.
	if err := p.validateRefs(ctx, repo.Root); err != nil {
		return result, err
	}
	finish, err := gitstate.BeginMutation(ctx, repo.Root, result.Paths)
	if err != nil {
		return result, err
	}
	defer func() {
		if historyErr := finish(ctx); historyErr != nil {
			err = errors.Join(err, fmt.Errorf("record Git history: %w", historyErr))
		}
	}()
	result.Attempted = true
	out, code, runErr := p.run(ctx, repo.Root)
	result.ExitCode = code
	result.Diagnostics = string(out)
	if runErr != nil {
		result.Diagnostics = runErr.Error() + "\n" + result.Diagnostics
	}
	// Cancellation can stop Git after it changes state. Observe with a fresh,
	// bounded context so the receipt still tells the user where the repository is.
	observation, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
	defer cancel()
	result.AfterObserved = false
	result.After, err = readRepositoryState(observation, repo.Root)
	notifyRepoChange(observation, repo.Root, repochange.HeadMoved)
	if err != nil {
		return result, fmt.Errorf("effect attempted; final Git state unavailable: %w", err)
	}
	result.AfterObserved = true
	result.Status = operationStatus(result.Before, result.After, code, runErr)
	if result.Status == "completed" || result.Status == "no_op" || result.Status == "conflicts" {
		if result.After.Head != p.expectedState.Head || result.After.Branch != p.expectedState.Branch || result.After.MergeHead != p.expectedState.MergeHead || result.After.Stash != p.expectedState.Stash {
			result.Status = "failed"
			result.Diagnostics += "\nGit state differs from the reviewed result."
		}
		if err := p.verifyFiles(observation, repo.Root); err != nil {
			result.Status = "failed"
			result.Diagnostics += "\n" + err.Error()
		}
	}
	return result, nil
}

func operationStatus(before, after RepositoryState, code int, err error) string {
	if len(after.Conflicts) > 0 {
		return "conflicts"
	}
	if err != nil || code != 0 {
		return "failed"
	}
	if reflect.DeepEqual(before, after) {
		return "no_op"
	}
	return "completed"
}

func readRepositoryState(ctx context.Context, dir string) (RepositoryState, error) {
	state := RepositoryState{Conflicts: []string{}}
	var err error
	state.Head, err = resolveCommit(ctx, dir, "HEAD")
	if err != nil {
		return state, err
	}
	out, code, err := gitexec.Run(ctx, dir, []string{"symbolic-ref", "--quiet", "HEAD"}, hermeticOpts(0))
	if err != nil || code > 1 {
		return state, fmt.Errorf("read symbolic HEAD: %w", err)
	}
	state.Branch = strings.TrimPrefix(strings.TrimSpace(string(out)), "refs/heads/")
	repo, _ := gitrepo.Discover(dir)
	index, err := readRestoreContent(filepath.Join(repo.GitDir, "index"))
	if err != nil {
		return state, err
	}
	state.Index = index.SHA256
	merge, err := readRestoreContent(filepath.Join(repo.GitDir, "MERGE_HEAD"))
	if err != nil {
		return state, err
	}
	state.MergeHead = strings.TrimSpace(string(merge.Bytes))
	stashLog, err := readRestoreContent(filepath.Join(repo.CommonDir, "logs", "refs", "stash"))
	if err != nil {
		return state, err
	}
	state.StashLog = stashLog.SHA256
	state.Stash, err = optionalObject(ctx, dir, "refs/stash")
	if err != nil {
		return state, err
	}
	conflicts, err := gitPaths(ctx, dir, "diff", "--name-only", "--diff-filter=U", "-z", "--")
	if err != nil {
		return state, err
	}
	state.Conflicts = conflicts
	_, entries, err := readPorcelainStatus(ctx, dir, false)
	state.Dirty = len(entries) > 0
	return state, err
}

// optionalObject distinguishes missing selectors by exit code; diagnostics are never parsed.
func optionalObject(ctx context.Context, dir, ref string) (string, error) {
	out, code, err := gitexec.Run(ctx, dir, []string{"rev-parse", "--verify", "--quiet", "--end-of-options", ref}, hermeticOpts(0))
	if err != nil {
		return "", err
	}
	if code == 1 {
		return "", nil
	}
	if code != 0 {
		return "", fmt.Errorf("resolve Git selector failed: %s", out)
	}
	oid := strings.TrimSpace(string(out))
	if !validObjectID(oid) {
		return "", fmt.Errorf("malformed Git selector identity")
	}
	return oid, nil
}

func gitPaths(ctx context.Context, dir string, args ...string) ([]string, error) {
	paths := []string{}
	err := gitexec.RunRecords(ctx, dir, args, hermeticOpts(0), func(raw []byte) error {
		p, err := parseRepoPath(raw)
		if err != nil {
			return err
		}
		if filepath.IsAbs(p) || p == ".." || strings.HasPrefix(filepath.Clean(p), ".."+string(filepath.Separator)) {
			return operationRefusal("invalid_repository_path", p)
		}
		paths = append(paths, p)
		return nil
	})
	return paths, err
}
