package security

import (
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/google/uuid"
	"github.com/lycaon/lycaon/internal/gitexec"
	"github.com/lycaon/lycaon/internal/llm"
	"github.com/lycaon/lycaon/internal/llm/compaction"
	"github.com/lycaon/lycaon/internal/presence"
	"github.com/lycaon/lycaon/internal/secretcap"
	"github.com/lycaon/lycaon/internal/secretmatch"
)

// projectRecipes covers projects, roots, Git repositories, managed secrets,
// ignores, and attachments.
var projectRecipes = map[string]successRecipe{
	"cloneProject": {prepare: projectRecipeClone},
	"detectFolder": {prepare: func(_ context.Context, s *conformanceSweep, _ *conformanceOp) (successInputs, error) {
		return successInputs{query: map[string]string{"path": s.fixture.projectDir}}, nil
	}},
	"attachProjectRoot": {prepare: func(_ context.Context, s *conformanceSweep, _ *conformanceOp) (successInputs, error) {
		dir, err := projectRecipeNewRootDir(s)
		if err != nil {
			return successInputs{}, err
		}
		return successInputs{fields: map[string]any{"path": dir}}, nil
	}},
	"updateProjectRoot": {prepare: func(ctx context.Context, s *conformanceSweep, _ *conformanceOp) (successInputs, error) {
		rootID, err := projectRecipeAttachedRoot(ctx, s)
		if err != nil {
			return successInputs{}, err
		}
		return projectRecipeJSON(map[string]string{"root_id": rootID}, nil, map[string]any{"label": "conformance-renamed"})
	}},
	// The fixture's own root carries every other probe, so only an attached
	// second root is detached.
	"detachProjectRoot": {prepare: func(ctx context.Context, s *conformanceSweep, _ *conformanceOp) (successInputs, error) {
		rootID, err := projectRecipeAttachedRoot(ctx, s)
		if err != nil {
			return successInputs{}, err
		}
		return successInputs{params: map[string]string{"root_id": rootID}}, nil
	}},

	"checkoutGit": {prepare: func(ctx context.Context, s *conformanceSweep, _ *conformanceOp) (successInputs, error) {
		repoID, err := projectRecipeGitRepo(ctx, s)
		if err != nil {
			return successInputs{}, err
		}
		return successInputs{
			params: map[string]string{"repo_id": repoID},
			fields: map[string]any{"branch": "conformance-" + projectRecipeShort(), "create": true},
		}, nil
	}},
	"pullGit": {prepare: projectRecipeRepoOnly},
	"pushGit": {prepare: projectRecipeRepoOnly},
	"draftGitCommitMessage": {prepare: func(ctx context.Context, s *conformanceSweep, _ *conformanceOp) (successInputs, error) {
		repoID, err := projectRecipeGitRepo(ctx, s)
		if err != nil {
			return successInputs{}, err
		}
		if err := os.WriteFile(filepath.Join(s.fixture.projectDir, "conformance-draft.txt"), []byte("draft "+projectRecipeShort()+"\n"), 0o644); err != nil {
			return successInputs{}, err
		}
		// Mock mode turns off utility model calls, so the drafter stands in
		// for the lite model; the handler and its diff are unchanged.
		s.host.Server.Admin.Git.CommitDrafter = compaction.MockSummarizer{Text: "Add the conformance draft file"}
		return successInputs{params: map[string]string{"repo_id": repoID}}, nil
	}},

	"updateProjectManagedSecret": {prepare: func(ctx context.Context, s *conformanceSweep, _ *conformanceOp) (successInputs, error) {
		id, err := projectRecipeSecret(ctx, s, s.secrets.issue("createProjectManagedSecret.secret_value"))
		if err != nil {
			return successInputs{}, err
		}
		return projectRecipeJSON(map[string]string{"secret_id": id}, nil, map[string]any{"name": "Conformance renamed " + projectRecipeShort()})
	}},
	"replaceProjectManagedSecretValue": {prepare: func(ctx context.Context, s *conformanceSweep, _ *conformanceOp) (successInputs, error) {
		id, err := projectRecipeSecret(ctx, s, s.secrets.issue("createProjectManagedSecret.secret_value"))
		if err != nil {
			return successInputs{}, err
		}
		return successInputs{
			params: map[string]string{"secret_id": id},
			fields: map[string]any{"secret_value": s.secrets.issue("replaceProjectManagedSecretValue.secret_value")},
		}, nil
	}},
	"holdProjectManagedSecret":   {prepare: projectRecipeFreshSecret},
	"revokeProjectManagedSecret": {prepare: projectRecipeFreshSecret},
	"listProjectManagedSecretUses": {
		prepare: func(ctx context.Context, s *conformanceSweep, _ *conformanceOp) (successInputs, error) {
			id, err := projectRecipeUsesSecret(ctx, s)
			if err != nil {
				return successInputs{}, err
			}
			return successInputs{params: map[string]string{"secret_id": id}}, nil
		},
		seed: projectRecipeSeedUses,
	},
	"beginProjectManagedSecretReveal": {prepare: func(ctx context.Context, s *conformanceSweep, _ *conformanceOp) (successInputs, error) {
		id, err := projectRecipeSecret(ctx, s, projectRecipePlainValue())
		if err != nil {
			return successInputs{}, err
		}
		return successInputs{params: map[string]string{"secret_id": id}, fields: map[string]any{"window_label": "main"}}, nil
	}},
	"completeProjectManagedSecretReveal": {prepare: projectRecipeCompleteReveal},

	"createProjectSecretIgnore": {prepare: func(_ context.Context, s *conformanceSweep, _ *conformanceOp) (successInputs, error) {
		rootID, err := projectRecipeFixtureRoot(s)
		if err != nil {
			return successInputs{}, err
		}
		return successInputs{fields: map[string]any{"root_id": rootID, "entry": projectRecipeSecretIgnoreEntry(uuid.NewString())}}, nil
	}},
	"deleteProjectSecretIgnore": {prepare: func(ctx context.Context, s *conformanceSweep, _ *conformanceOp) (successInputs, error) {
		rootID, err := projectRecipeFixtureRoot(s)
		if err != nil {
			return successInputs{}, err
		}
		entryID := uuid.NewString()
		if _, err := s.prerequisite(ctx, "createProjectSecretIgnore", successInputs{
			fields: map[string]any{"root_id": rootID, "entry": projectRecipeSecretIgnoreEntry(entryID)},
		}); err != nil {
			return successInputs{}, err
		}
		return successInputs{params: map[string]string{"entry_id": entryID}, query: map[string]string{"root_id": rootID}}, nil
	}},
	// Candidates exist only while an approval holds a detection; the recipe
	// offers one the way that approval does.
	"getSecretIgnoreCandidate": {prepare: func(ctx context.Context, s *conformanceSweep, _ *conformanceOp) (successInputs, error) {
		if s.host == nil {
			return successInputs{}, errors.New("no host to offer a secret review")
		}
		id, _ := s.host.Server.Admin.Project.Secrets.SecretIgnores.Reviews.Offer(ctx, s.fixture.project.ID, "conformance-candidate-"+uuid.NewString())
		if id == "" {
			return successInputs{}, errors.New("the host declined the secret review")
		}
		return successInputs{params: map[string]string{"candidate_id": id}}, nil
	}},
	"createProjectFindingIgnore": {prepare: func(_ context.Context, _ *conformanceSweep, _ *conformanceOp) (successInputs, error) {
		return successInputs{fields: map[string]any{"path": "conformance-ignored-" + projectRecipeShort()}}, nil
	}},
	"deleteProjectFindingIgnore": {prepare: projectRecipeDeleteFindingIgnore},

	"uploadAttachment": {prepare: func(_ context.Context, _ *conformanceSweep, _ *conformanceOp) (successInputs, error) {
		return successInputs{
			query:       map[string]string{"filename": "conformance.txt"},
			body:        []byte("conformance attachment\n"),
			contentType: "application/octet-stream",
		}, nil
	}},
	"repairLocalGeneration": {prepare: func(_ context.Context, _ *conformanceSweep, _ *conformanceOp) (successInputs, error) {
		return successInputs{params: map[string]string{"provider_id": llm.IncludedProviderID}}, nil
	}},
}

const (
	projectRecipeRootPrefix   = "conformance-root-"
	projectRecipeSecretPrefix = "/v1/projects/{id}/secrets/{secret_id}"
)

func projectRecipeShort() string { return uuid.NewString()[:8] }

// projectRecipePlainValue is a secret value the sweep does not scan for, for
// the reveal that answers it by design.
func projectRecipePlainValue() string {
	return "pwreveal" + strings.ReplaceAll(uuid.NewString(), "-", "")
}

// projectRecipeJSON sends body as the whole request, so no generated
// optional field rides along.
func projectRecipeJSON(params, query map[string]string, body map[string]any) (successInputs, error) {
	raw, err := json.Marshal(body)
	if err != nil {
		return successInputs{}, err
	}
	return successInputs{params: params, query: query, body: raw, contentType: "application/json"}, nil
}

func projectRecipeFixtureRoot(s *conformanceSweep) (string, error) {
	if len(s.fixture.project.Roots) == 0 {
		return "", errors.New("the fixture project has no root")
	}
	return s.fixture.project.Roots[0].ID, nil
}

func projectRecipeNewRootDir(s *conformanceSweep) (string, error) {
	if s.host == nil {
		return "", errors.New("no host to create a folder under")
	}
	return s.host.ProjectDir(s.fixture.t, projectRecipeRootPrefix+projectRecipeShort()), nil
}

// projectRecipeAttachedRoot answers an attached root other than the
// fixture's, attaching one when none is.
func projectRecipeAttachedRoot(ctx context.Context, s *conformanceSweep) (string, error) {
	current, err := s.prerequisite(ctx, "getProject", successInputs{})
	if err != nil {
		return "", err
	}
	if id := projectRecipeRootIn(current); id != "" {
		return id, nil
	}
	dir, err := projectRecipeNewRootDir(s)
	if err != nil {
		return "", err
	}
	attached, err := s.prerequisite(ctx, "attachProjectRoot", successInputs{fields: map[string]any{"path": dir}})
	if err != nil {
		return "", err
	}
	if id := projectRecipeRootIn(attached); id != "" {
		return id, nil
	}
	return "", errors.New("the attached root is missing from the project")
}

func projectRecipeRootIn(project map[string]any) string {
	roots, _ := project["roots"].([]any)
	for _, raw := range roots {
		root, _ := raw.(map[string]any)
		path, _ := root["path"].(string)
		id, _ := root["id"].(string)
		if id != "" && strings.HasPrefix(filepath.Base(path), projectRecipeRootPrefix) {
			return id
		}
	}
	return ""
}

func projectRecipeClone(ctx context.Context, s *conformanceSweep, _ *conformanceOp) (successInputs, error) {
	if s.host == nil {
		return successInputs{}, errors.New("no host to create folders under")
	}
	source := s.host.ProjectDir(s.fixture.t, "conformance-clone-source")
	if _, err := projectRecipeRunGit(ctx, source, "rev-parse", "--verify", "HEAD"); err != nil {
		if _, err := projectRecipeRunGit(ctx, source, "init"); err != nil {
			return successInputs{}, err
		}
		if _, err := projectRecipeRunGit(ctx, source, "commit", "--allow-empty", "-m", "conformance"); err != nil {
			return successInputs{}, err
		}
	}
	return successInputs{fields: map[string]any{
		"url":        source,
		"parent_dir": s.host.ProjectDir(s.fixture.t, "conformance-clones"),
		"name":       "clone-" + projectRecipeShort(),
	}}, nil
}

func projectRecipeRunGit(ctx context.Context, dir string, args ...string) (string, error) {
	out, code, err := gitexec.Run(ctx, dir, args, gitexec.Opts{
		Profile:     gitexec.ProfileHermetic,
		Identity:    &gitexec.Identity{Name: "Painted Wolf test", Email: "test@paintedwolf.dev"},
		ExtraConfig: []string{"maintenance.auto=false"},
	})
	if err != nil {
		return "", fmt.Errorf("git %v: %w", args, err)
	}
	if code != 0 {
		return "", fmt.Errorf("git %v: exit %d: %s", args, code, strings.TrimSpace(string(out)))
	}
	return strings.TrimSpace(string(out)), nil
}

// projectRecipeGitRepo makes the fixture root a repository through the API,
// then gives its current branch a commit and an upstream in a local bare
// remote beside it, so push and pull reach a real remote.
func projectRecipeGitRepo(ctx context.Context, s *conformanceSweep) (string, error) {
	if s.host == nil {
		return "", errors.New("no host to place a remote beside the fixture")
	}
	rootID, err := projectRecipeFixtureRoot(s)
	if err != nil {
		return "", err
	}
	entry, err := s.prerequisite(ctx, "createGitRepo", successInputs{fields: map[string]any{"root_id": rootID}})
	if err != nil {
		return "", err
	}
	repoID, _ := entry["repo_id"].(string)
	if repoID == "" {
		return "", errors.New("createGitRepo answered no repo_id")
	}
	dir := s.fixture.projectDir
	if _, err := projectRecipeRunGit(ctx, dir, "rev-parse", "--verify", "HEAD"); err != nil {
		if _, err := projectRecipeRunGit(ctx, dir, "commit", "--allow-empty", "-m", "conformance"); err != nil {
			return "", err
		}
	}
	if _, err := projectRecipeRunGit(ctx, dir, "remote", "get-url", "origin"); err != nil {
		remote := filepath.Join(filepath.Dir(dir), "conformance-remote.git")
		if err := os.MkdirAll(remote, 0o755); err != nil {
			return "", err
		}
		if _, err := projectRecipeRunGit(ctx, remote, "init", "--bare"); err != nil {
			return "", err
		}
		if _, err := projectRecipeRunGit(ctx, dir, "remote", "add", "origin", remote); err != nil {
			return "", err
		}
	}
	if _, err := projectRecipeRunGit(ctx, dir, "rev-parse", "--abbrev-ref", "--symbolic-full-name", "@{upstream}"); err != nil {
		if _, err := projectRecipeRunGit(ctx, dir, "push", "--set-upstream", "origin", "HEAD"); err != nil {
			return "", err
		}
	}
	return repoID, nil
}

func projectRecipeRepoOnly(ctx context.Context, s *conformanceSweep, _ *conformanceOp) (successInputs, error) {
	repoID, err := projectRecipeGitRepo(ctx, s)
	if err != nil {
		return successInputs{}, err
	}
	return successInputs{params: map[string]string{"repo_id": repoID}}, nil
}

// projectRecipeSecret stores value as a new project secret and answers its id.
func projectRecipeSecret(ctx context.Context, s *conformanceSweep, value string) (string, error) {
	short := projectRecipeShort()
	created, err := s.prerequisite(ctx, "createProjectManagedSecret", successInputs{fields: map[string]any{
		"operation_id": uuid.NewString(),
		"name":         "Conformance " + short,
		"purpose":      "Conformance recipe " + short,
		"secret_value": value,
	}})
	if err != nil {
		return "", err
	}
	reference, _ := created["reference"].(string)
	id, ok := secretmatch.ParseReferenceToken(reference)
	if !ok {
		return "", fmt.Errorf("created secret answered reference %q", reference)
	}
	return id, nil
}

func projectRecipeFreshSecret(ctx context.Context, s *conformanceSweep, _ *conformanceOp) (successInputs, error) {
	id, err := projectRecipeSecret(ctx, s, s.secrets.issue("createProjectManagedSecret.secret_value"))
	if err != nil {
		return successInputs{}, err
	}
	return successInputs{params: map[string]string{"secret_id": id}}, nil
}

// projectRecipeUsesSecret binds one secret to the secret address, so the
// page probes and walks list a real history.
func projectRecipeUsesSecret(ctx context.Context, s *conformanceSweep) (string, error) {
	if id, ok := s.values.byPrefix[projectRecipeSecretPrefix]; ok {
		return id, nil
	}
	id, err := projectRecipeSecret(ctx, s, s.secrets.issue("createProjectManagedSecret.secret_value"))
	if err != nil {
		return "", err
	}
	s.values.bind(projectRecipeSecretPrefix, "", id)
	return id, nil
}

// projectRecipeSeedUses records two substitution attempts the way an agent
// tool call does; no route records a use.
func projectRecipeSeedUses(ctx context.Context, s *conformanceSweep) error {
	if s.host == nil {
		return errors.New("no host to record secret uses")
	}
	id, err := projectRecipeUsesSecret(ctx, s)
	if err != nil {
		return err
	}
	service := s.host.Server.Admin.Project.Secrets.ManagedSecrets
	for i := range 2 {
		resolution, err := service.Resolve(ctx, map[string]any{"value": secretmatch.ReferenceToken(id)}, secretcap.ResolveContext{
			ProjectID: s.fixture.project.ID, ChatSessionID: s.fixture.session.ID, SessionID: s.fixture.session.ID,
			ToolName: "conformance_seed", ToolCallID: fmt.Sprintf("conformance-use-%d", i),
		})
		if err != nil {
			return fmt.Errorf("resolve the uses secret: %w", err)
		}
		resolution.Finish(ctx)
	}
	return nil
}

func projectRecipeCompleteReveal(ctx context.Context, s *conformanceSweep, _ *conformanceOp) (successInputs, error) {
	key := s.fixture.presenceKey
	if key == nil {
		return successInputs{}, errors.New("the fixture holds no presence key")
	}
	id, err := projectRecipeSecret(ctx, s, projectRecipePlainValue())
	if err != nil {
		return successInputs{}, err
	}
	challenge, err := s.prerequisite(ctx, "beginProjectManagedSecretReveal", successInputs{
		params: map[string]string{"secret_id": id}, fields: map[string]any{"window_label": "main"},
	})
	if err != nil {
		return successInputs{}, err
	}
	challengeID, _ := challenge["challenge_id"].(string)
	payload, _ := challenge["proof_payload"].(string)
	if challengeID == "" || payload == "" {
		return successInputs{}, errors.New("the reveal challenge carried no id or payload")
	}
	signature := ed25519.Sign(key, presence.SigningMessage(payload, presence.AuthenticatorMacOS))
	return successInputs{
		params: map[string]string{"secret_id": id, "challenge_id": challengeID},
		fields: map[string]any{
			"authenticator": presence.AuthenticatorMacOS,
			"signature":     base64.RawURLEncoding.EncodeToString(signature),
		},
	}, nil
}

// projectRecipeSecretIgnoreEntry names a public value no managed secret
// holds, so the host does not refuse it as protected.
func projectRecipeSecretIgnoreEntry(id string) map[string]any {
	return map[string]any{"id": id, "value": "conformance-public-" + uuid.NewString(), "reason": "Conformance recipe"}
}

func projectRecipeDeleteFindingIgnore(ctx context.Context, s *conformanceSweep, _ *conformanceOp) (successInputs, error) {
	path := "conformance-ignored-" + projectRecipeShort()
	catalog, err := s.prerequisite(ctx, "createProjectFindingIgnore", successInputs{fields: map[string]any{"path": path}})
	if err != nil {
		return successInputs{}, err
	}
	rules, _ := catalog["rules"].([]any)
	for _, raw := range rules {
		rule, _ := raw.(map[string]any)
		id, _ := rule["id"].(string)
		withdrawable, _ := rule["withdrawable"].(bool)
		if rule["path"] == path && withdrawable && id != "" {
			return successInputs{params: map[string]string{"entry_id": id}}, nil
		}
	}
	return successInputs{}, fmt.Errorf("the ignore catalog holds no withdrawable entry for %s", path)
}
