package gitcmd

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"github.com/lycaon/lycaon/internal/confine"
	"github.com/lycaon/lycaon/internal/egressgate"
	"github.com/lycaon/lycaon/internal/git"
	"github.com/lycaon/lycaon/internal/gitargv"
	"github.com/lycaon/lycaon/internal/gitexec"
	"github.com/lycaon/lycaon/internal/gitrepo"
	"github.com/lycaon/lycaon/internal/toolcontract"
	"github.com/lycaon/lycaon/internal/toolrejection"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/tools/native"
)

func runNetwork(ctx context.Context, p Prepared, tc tools.ToolContext, dir string, repo gitrepo.Repo, out *meteredOutput, stderr io.Writer, certainty toolcontract.EffectCertainty) (toolcontract.ProducerResult, error) {
	if err := authorizeRepoNetwork(ctx, p, tc, dir, repo.Root, stderr); err != nil {
		return settleMutationError(err, toolcontract.NoEffect)
	}
	diagnostics, code, err := p.owners.Git.NetworkCommand(ctx, repo.Root, p.command.args, out)
	if out.err != nil {
		return settleMutationError(out.err, certainty)
	}
	if len(diagnostics) > 0 {
		_, _ = stderr.Write(diagnostics)
	}
	if err != nil {
		return settleGitError(err, stderr, toolcontract.NoEffect)
	}
	if code != 0 {
		return toolcontract.ProducerResult{Status: code, Terminal: toolcontract.ProducerCompleted, Complete: true, EffectCertainty: toolcontract.NoEffect}, nil
	}
	return toolcontract.ProducerResult{Status: 0, Terminal: toolcontract.ProducerCompleted, Complete: true, PublicationEligible: true, EffectCertainty: certainty}, nil
}

func runPull(ctx context.Context, p Prepared, tc tools.ToolContext, dir string, repo gitrepo.Repo, out *meteredOutput, stderr io.Writer) (toolcontract.ProducerResult, error) {
	if err := authorizeRepoNetwork(ctx, p, tc, dir, repo.Root, stderr); err != nil {
		return settleMutationError(err, toolcontract.NoEffect)
	}
	fetchArgs := []string{"fetch"}
	if has(p.Argv, "-q") {
		fetchArgs = append(fetchArgs, "-q")
	}
	fetchArgs = append(fetchArgs, p.Argv.Operands().Values()...)
	diagnostics, code, err := p.owners.Git.NetworkCommand(ctx, repo.Root, fetchArgs, out)
	if out.err != nil {
		return settleMutationError(out.err, toolcontract.EffectUnknown)
	}
	if len(diagnostics) > 0 {
		_, _ = stderr.Write(diagnostics)
	}
	if err != nil {
		return settleGitError(err, stderr, toolcontract.NoEffect)
	}
	if code != 0 {
		return toolcontract.ProducerResult{Status: code, Terminal: toolcontract.ProducerCompleted, Complete: true, EffectCertainty: toolcontract.NoEffect}, nil
	}
	req := git.OperationRequest{Kind: "merge", Mode: "ff", Ref: "FETCH_HEAD"}
	if has(p.Argv, "--rebase") {
		req = git.OperationRequest{Kind: "rebase", Ref: "FETCH_HEAD"}
	} else if has(p.Argv, "--ff-only") {
		req.Mode = "ff_only"
	}
	mut := native.GitMutations{Git: p.owners.Git, Boundary: p.owners.Boundary}
	res, opErr := mut.Operate(ctx, tc, req)
	return settleOperate(p, res, out, stderr, has(p.Argv, "-q"), opErr)
}

func runClone(ctx context.Context, p Prepared, tc tools.ToolContext, out *meteredOutput, stderr io.Writer) (toolcontract.ProducerResult, error) {
	dir, status, err := resolveCommandDir(ctx, p, stderr, true)
	if err != nil || status != 0 {
		return settle(status, err, p.command.spec.Name)
	}
	ops := p.Argv.Operands().Values()
	rawURL := ops[0]
	if err := authorizeRemote(ctx, p, tc, dir, rawURL, stderr); err != nil {
		return settleMutationError(err, toolcontract.NoEffect)
	}
	var destTarget string
	if len(ops) == 2 {
		destTarget = ops[1]
	} else {
		destTarget = defaultCloneDirName(rawURL)
		if destTarget == "" {
			return settleMutationError(operandInvalid(p.command.spec, rawURL, "a repository URL with a directory name or explicit DIR"), toolcontract.NoEffect)
		}
	}
	if !filepath.IsAbs(destTarget) {
		destTarget = filepath.Join(dir, destTarget)
	}
	boundDest, err := p.paths.Bind(ctx, destTarget, true)
	if err != nil {
		return settleMutationError(err, toolcontract.NoEffect)
	}
	var flags []string
	if has(p.Argv, "-q") {
		flags = append(flags, "-q")
	}
	if branch, ok := optionValue(p.Argv, "-b"); ok {
		if err := gitargv.ValidateRefArg(branch); err != nil {
			return settleMutationError(operandInvalid(p.command.spec, branch, "a valid branch name"), toolcontract.NoEffect)
		}
		flags = append(flags, "-b", branch)
	}
	if depth, ok := optionValue(p.Argv, "--depth"); ok {
		flags = append(flags, "--depth", depth)
	}
	if has(p.Argv, "--single-branch") {
		flags = append(flags, "--single-branch")
	}
	resolvedURL := resolveLocalCloneSource(dir, rawURL)
	diagnostics, code, err := git.CloneCommand(ctx, resolvedURL, boundDest.Resolved.Abs, flags, out)
	if out.err != nil {
		return settleMutationError(out.err, toolcontract.EffectUnknown)
	}
	if len(diagnostics) > 0 {
		_, _ = stderr.Write(diagnostics)
	}
	if err != nil {
		if errors.Is(err, git.ErrCloneDestinationExists) {
			_, _ = fmt.Fprintf(stderr, "fatal: destination path '%s' already exists and is not an empty directory.\n", filepath.Base(boundDest.Resolved.Abs))
			return toolcontract.ProducerResult{Status: 128, Terminal: toolcontract.ProducerCompleted, Complete: true, EffectCertainty: toolcontract.NoEffect}, nil
		}
		return settleGitError(err, stderr, toolcontract.NoEffect)
	}
	if code != 0 {
		return toolcontract.ProducerResult{Status: code, Terminal: toolcontract.ProducerCompleted, Complete: true, EffectCertainty: toolcontract.NoEffect}, nil
	}
	return toolcontract.ProducerResult{Status: 0, Terminal: toolcontract.ProducerCompleted, Complete: true, PublicationEligible: true, EffectCertainty: toolcontract.EffectCompleted}, nil
}

func authorizeRepoNetwork(ctx context.Context, p Prepared, tc tools.ToolContext, dir, repoRoot string, stderr io.Writer) error {
	if p.command.spec.Name == "git fetch" && has(p.Argv, "--all") {
		if remotes, ok := gitexec.ListRemotes(ctx, repoRoot); ok {
			for _, r := range remotes {
				if err := authorizeRemote(ctx, p, tc, dir, r.URL, stderr); err != nil {
					return err
				}
			}
		}
	}
	remoteURL := p.owners.Git.RemoteURL(ctx, repoRoot, remoteQueryArgs(p))
	return authorizeRemote(ctx, p, tc, dir, remoteURL, stderr)
}

func remoteQueryArgs(p Prepared) []string {
	sub := firstOrEmpty(p.command.args)
	if ops := p.Argv.Operands().Values(); len(ops) > 0 {
		return []string{sub, ops[0]}
	}
	return []string{sub}
}

func authorizeRemote(ctx context.Context, p Prepared, tc tools.ToolContext, dir, rawURL string, stderr io.Writer) error {
	rawURL = strings.TrimSpace(rawURL)
	if rawURL == "" {
		return nil
	}
	if err := gitargv.ValidateCloneURL(rawURL); err != nil {
		writeLine(stderr, fmt.Sprintf("fatal: %s: invalid remote '%s' [COMMAND_OPERAND_INVALID]", p.command.spec.Name, rawURL))
		return &toolrejection.ToolReject{Code: "COMMAND_OPERAND_INVALID", Data: map[string]any{"command": p.command.spec.Name, "word": rawURL, "expected": "a safe remote URL", "reason": "The remote is not a URL or path Git can fetch from safely."}}

	}
	host := remoteNetworkHost(rawURL)
	if host == "" {
		return nil
	}
	if egressgate.From(ctx) == nil {
		sessionID := tc.Identity.SessionID
		if sessionID == "" {
			sessionID = "gitcmd"
		}
		actionID := tc.EffectActionID()
		ctx = egressgate.WithAttribution(ctx, confine.EgressCommand{
			SessionID:     sessionID,
			RootSessionID: tc.ChatSessionID(),
			ProjectID:     tc.Identity.ProjectID,
			ProjectDir:    dir,
			ToolCallID:    tc.Identity.ToolCallID,
			Image:         "git",
			ToolName:      p.command.spec.Name,
			ActionID:      actionID,
		})
		defer confine.ForgetEgressAction(sessionID, actionID)
	}
	if err := egressgate.AwaitHost(ctx, host); err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return err
		}
		writeLine(stderr, fmt.Sprintf("fatal: %s: remote host '%s' denied [GIT_REMOTE_HOST_DENIED]", p.command.spec.Name, host))
		return &toolrejection.ToolReject{Code: "GIT_REMOTE_HOST_DENIED", Data: map[string]any{"command": p.command.spec.Name, "host": host}}
	}
	return nil
}

func remoteNetworkHost(rawURL string) string {
	lower := strings.ToLower(rawURL)
	if strings.HasPrefix(lower, "file://") {
		return ""
	}
	if strings.Contains(lower, "://") {
		u, err := url.Parse(rawURL)
		if err != nil {
			return rawURL
		}
		return strings.TrimSpace(u.Hostname())
	}
	hostPart := rawURL
	if at := strings.IndexByte(hostPart, '@'); at >= 0 && (strings.IndexByte(hostPart, '/') < 0 || at < strings.IndexByte(hostPart, '/')) {
		hostPart = hostPart[at+1:]
	}
	if strings.HasPrefix(hostPart, "[") {
		if closeIdx := strings.Index(hostPart, "]:"); closeIdx > 1 {
			return strings.TrimSpace(hostPart[1:closeIdx])
		}
	}
	if colon := strings.IndexByte(rawURL, ':'); colon > 0 && !strings.Contains(rawURL[:colon], "/") && !strings.Contains(rawURL[:colon], `\`) {
		host := rawURL[:colon]
		if at := strings.LastIndexByte(host, '@'); at >= 0 {
			host = host[at+1:]
		}
		return strings.TrimSpace(host)
	}
	return ""
}

func resolveLocalCloneSource(dir, rawURL string) string {
	if strings.Contains(rawURL, "://") || remoteNetworkHost(rawURL) != "" || filepath.IsAbs(rawURL) {
		return rawURL
	}
	candidate := filepath.Join(dir, rawURL)
	if _, err := os.Stat(candidate); err == nil {
		return candidate
	}
	return rawURL
}

func defaultCloneDirName(rawURL string) string {
	trimmed := strings.TrimRight(rawURL, "/")
	if strings.Contains(trimmed, "://") {
		if u, err := url.Parse(trimmed); err == nil && u.Path != "" {
			trimmed = strings.TrimRight(u.Path, "/")
		}
	} else if colon := strings.LastIndexByte(trimmed, ':'); colon >= 0 {
		trimmed = trimmed[colon+1:]
	}
	base := filepath.Base(trimmed)
	base = strings.TrimSuffix(base, ".git")
	if base == "" || base == "." || base == "/" {
		return ""
	}
	return base
}
