package governance

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/lycaon/lycaon/internal/agentpolicy"
	"github.com/lycaon/lycaon/internal/coordinator/anchor"
	"github.com/lycaon/lycaon/internal/prompts"
	"github.com/lycaon/lycaon/internal/settingsoverlay"
)

// ScopedAgentsMD retains the workspace identity of a loaded policy file.
type ScopedAgentsMD struct {
	Revision agentpolicy.Revision
	Content  string
}

// ResolveScopes reads each applicable file once across resolved workspaces.
func ResolveScopes(ctx context.Context, scopes []agentpolicy.Scope, maxBodyBytes int) ([]ScopedAgentsMD, error) {
	var files []ScopedAgentsMD
	seen := map[agentpolicy.Revision]bool{}
	failedReads := map[agentpolicy.Revision]error{}
	var failures []error
	for _, scope := range scopes {
		if err := ctx.Err(); err != nil {
			return files, err
		}
		root, path, err := policyLocation(scope)
		if err != nil {
			failures = append(failures, &agentpolicy.ScopeError{Scope: scope, Cause: err})
			continue
		}
		chain, err := ResolveChainMetadata(root, path)
		if err != nil {
			failures = append(failures, &agentpolicy.ScopeError{Scope: scope, Cause: err})
			continue
		}
		for _, file := range chain {
			revision := agentpolicy.Revision{RootID: scope.RootID, RootPath: root, Path: file.Path}
			if seen[revision] {
				if prior := failedReads[revision]; prior != nil {
					failures = append(failures, &agentpolicy.ScopeError{Scope: scope, Cause: prior})
				}
				continue
			}
			seen[revision] = true
			content, readErr := readAgentsMDBody(root, file.Path, maxBodyBytes)
			if readErr != nil {
				failedReads[revision] = readErr
				failures = append(failures, &agentpolicy.ScopeError{Scope: scope, Cause: readErr})
				continue
			}
			digest := sha256.Sum256([]byte(content))
			revision.Digest = hex.EncodeToString(digest[:])
			files = append(files, ScopedAgentsMD{Revision: revision, Content: CapAgentsMDBody(content, normalizeAgentsMDMaxBodyBytes(maxBodyBytes), file.Path)})
		}
	}
	return files, errors.Join(failures...)
}

func policyLocation(scope agentpolicy.Scope) (string, string, error) {
	if strings.TrimSpace(scope.RootID) == "" {
		return "", "", fmt.Errorf("policy root identity required")
	}
	if !filepath.IsAbs(scope.RootPath) {
		return "", "", fmt.Errorf("policy workspace must be absolute")
	}
	root, err := canonicalRoot(scope.RootPath)
	if err != nil {
		return "", "", err
	}
	path := scope.Path
	if path == "" {
		path = "."
	}
	if filepath.IsAbs(path) || strings.ContainsRune(path, 0) {
		return "", "", fmt.Errorf("policy scope must be root-relative")
	}
	return root, path, nil
}

// BuildScopedInject labels a policy with its own jurisdiction, not the last tool path.
func BuildScopedInject(ctx context.Context, renderer *prompts.InjectRenderer, sessionID, label string, file ScopedAgentsMD) (AgentsMDInject, error) {
	if renderer == nil {
		return AgentsMDInject{}, fmt.Errorf("inject renderer not configured")
	}
	path := "@" + label + "/" + file.Revision.Path
	jurisdiction := "@" + label + "/" + filepath.ToSlash(filepath.Dir(file.Revision.Path))
	if file.Revision.Path == settingsoverlay.Rel("AGENTS.md") {
		jurisdiction = "@" + label + "/."
	}
	block, err := anchor.RenderInform(ctx, anchor.InjectAgentsMD, anchor.MatchContext{Surface: "coordinator", SessionID: sessionID}, renderer, map[string]any{
		"mode": "chain", "target_path": jurisdiction,
		"chain": []map[string]any{{"path": path, "content": file.Content}},
	})
	if err != nil {
		return AgentsMDInject{}, err
	}
	return AgentsMDInject{Content: strings.TrimSpace(block), Paths: []string{file.Revision.Path}}, nil
}
