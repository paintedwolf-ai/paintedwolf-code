package gitcmd

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"slices"
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
	admitted, err := authorizeRepoNetwork(ctx, p, tc, repo.Root, stderr)
	if err != nil {
		return settleMutationError(err, toolcontract.NoEffect)
	}
	ctx, done, err := bindNetworkLaunch(ctx, p, tc, admitted)
	if err != nil {
		return settleMutationError(err, toolcontract.NoEffect)
	}
	failed := true
	defer func() { done(failed) }()
	diagnostics, code, err := p.owners.Git.NetworkCommand(ctx, repo.Root, p.command.args, out)
	failed = err != nil || code != 0 || out.err != nil
	reportNetworkRead(ctx, admitted, networkRead{wroteStdout: out.written > 0, wroteStderr: len(diagnostics) > 0})
	if out.err != nil {
		return settleMutationError(out.err, certainty)
	}
	if len(diagnostics) > 0 {
		_, _ = stderr.Write(diagnostics)
	}
	// A failed fetch or push may already have updated some refs here or on
	// the remote; only a read-only command fails without effect.
	effect := toolcontract.EffectUnknown
	if certainty == toolcontract.NoEffect {
		effect = toolcontract.NoEffect
	}
	if err != nil {
		return settleGitError(err, stderr, effect)
	}
	if code != 0 {
		return toolcontract.ProducerResult{Status: code, Terminal: toolcontract.ProducerCompleted, Complete: true, EffectCertainty: effect}, nil
	}
	return toolcontract.ProducerResult{Status: 0, Terminal: toolcontract.ProducerCompleted, Complete: true, PublicationEligible: true, EffectCertainty: certainty}, nil
}

func runPull(ctx context.Context, p Prepared, tc tools.ToolContext, dir string, repo gitrepo.Repo, out *meteredOutput, stderr io.Writer) (toolcontract.ProducerResult, error) {
	admitted, err := authorizeRepoNetwork(ctx, p, tc, repo.Root, stderr)
	if err != nil {
		return settleMutationError(err, toolcontract.NoEffect)
	}
	ctx, done, err := bindNetworkLaunch(ctx, p, tc, admitted)
	if err != nil {
		return settleMutationError(err, toolcontract.NoEffect)
	}
	failed := true
	defer func() { done(failed) }()
	fetchArgs := []string{"fetch"}
	if has(p.Argv, "-q") {
		fetchArgs = append(fetchArgs, "-q")
	}
	fetchArgs = append(fetchArgs, p.Argv.Operands().Values()...)
	diagnostics, code, err := p.owners.Git.NetworkCommand(ctx, repo.Root, fetchArgs, out)
	failed = err != nil || code != 0 || out.err != nil
	reportNetworkRead(ctx, admitted, networkRead{wroteStdout: out.written > 0, wroteStderr: len(diagnostics) > 0})
	if out.err != nil {
		return settleMutationError(out.err, toolcontract.EffectUnknown)
	}
	if len(diagnostics) > 0 {
		_, _ = stderr.Write(diagnostics)
	}
	// The fetch may have updated remote-tracking refs before it failed.
	if err != nil {
		return settleGitError(err, stderr, toolcontract.EffectUnknown)
	}
	if code != 0 {
		return toolcontract.ProducerResult{Status: code, Terminal: toolcontract.ProducerCompleted, Complete: true, EffectCertainty: toolcontract.EffectUnknown}, nil
	}
	req := git.OperationRequest{Kind: "merge", Mode: "ff", Ref: "FETCH_HEAD"}
	if has(p.Argv, "--rebase") {
		req = git.OperationRequest{Kind: "rebase", Ref: "FETCH_HEAD"}
	} else if has(p.Argv, "--ff-only") {
		req.Mode = "ff_only"
	}
	mut := native.GitMutations{Git: p.owners.Git, Boundary: p.owners.Boundary, PrivateGit: p.owners.PrivateGit}
	res, opErr := mut.Operate(ctx, tc, req)
	result, err := settleOperate(p, res, out, stderr, has(p.Argv, "-q"), opErr)
	// The merge or rebase places fetched content in the files it changed, and
	// its summary names them.
	reportNetworkRead(ctx, admitted, networkRead{wroteStdout: out.written > 0, files: mergedPaths(repo.Root, res.Paths)})
	return result, err
}

func runClone(ctx context.Context, p Prepared, tc tools.ToolContext, out *meteredOutput, stderr io.Writer) (toolcontract.ProducerResult, error) {
	dir, status, err := resolveCommandDir(ctx, p, stderr, true)
	if err != nil || status != 0 {
		return settle(status, err, p.command.spec.Name)
	}
	ops := p.Argv.Operands().Values()
	rawURL := ops[0]
	admitted := admittedRemotes{urls: []string{rawURL}}
	if err := authorizeRemote(ctx, p, tc, dir, rawURL, false, &admitted, stderr); err != nil {
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
	admitted.writes = append(admitted.writes, filepath.Dir(boundDest.Resolved.Abs))
	ctx, done, err := bindNetworkLaunch(ctx, p, tc, admitted)
	if err != nil {
		return settleMutationError(err, toolcontract.NoEffect)
	}
	failed := true
	defer func() { done(failed) }()
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
	failed = err != nil || code != 0 || out.err != nil
	reportNetworkRead(ctx, admitted, networkRead{wroteStdout: out.written > 0, wroteStderr: len(diagnostics) > 0, trees: []string{boundDest.Resolved.Abs}})
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

// admittedRemotes is what a network command was authorized to reach: its
// remote URLs, and the local repositories among them it may read or write.
type admittedRemotes struct {
	urls          []string
	reads, writes []string
}

// authorizeRepoNetwork admits every URL the command will contact, as Git
// itself resolves them, then confirms the resolution still holds once any
// approval wait ends.
func authorizeRepoNetwork(ctx context.Context, p Prepared, tc tools.ToolContext, repoRoot string, stderr io.Writer) (admittedRemotes, error) {
	intent := gitexec.RemoteIntent{Push: p.command.spec.Name == "git push"}
	if p.command.spec.Name == "git fetch" && has(p.Argv, "--all") {
		intent.All = true
	} else if ops := p.Argv.Operands().Values(); len(ops) > 0 {
		intent.Remote = ops[0]
	}
	urls, err := gitexec.ResolveRemoteURLs(ctx, repoRoot, intent)
	if err != nil {
		writeLine(stderr, fmt.Sprintf("fatal: %s: '%s' does not appear to be a git repository [COMMAND_OPERAND_INVALID]", p.command.spec.Name, intent.Remote))
		return admittedRemotes{}, &toolrejection.ToolReject{Code: "COMMAND_OPERAND_INVALID", Data: map[string]any{"command": p.command.spec.Name, "word": intent.Remote, "expected": "a configured remote or a repository URL", "reason": "Git cannot resolve the remote to a URL."}}
	}
	admitted := admittedRemotes{urls: urls}
	for _, rawURL := range urls {
		if err := authorizeRemote(ctx, p, tc, repoRoot, rawURL, intent.Push, &admitted, stderr); err != nil {
			return admittedRemotes{}, err
		}
	}
	if current, err := gitexec.ResolveRemoteURLs(ctx, repoRoot, intent); err != nil || !slices.Equal(current, urls) {
		writeLine(stderr, fmt.Sprintf("fatal: %s: the remote configuration changed while it was being authorized [GIT_REMOTE_CHANGED]", p.command.spec.Name))
		return admittedRemotes{}, &toolrejection.ToolReject{Code: "GIT_REMOTE_CHANGED", Retryable: true, Data: map[string]any{"command": p.command.spec.Name}}
	}
	return admitted, nil
}

// bindNetworkLaunch binds the command's confined launch for what was
// admitted: a credential service scoped to its HTTP recipients, answering
// once the call holds the credential-store grant, the local repositories it
// may read or write, and the user's SSH agent when the call holds that socket
// capability. done settles the launch with whether Git failed.
func bindNetworkLaunch(ctx context.Context, p Prepared, tc tools.ToolContext, admitted admittedRemotes) (context.Context, func(failed bool), error) {
	credentials, err := gitexec.StartAgentCredentials(ctx, tc.Execution.CredentialStore)
	if err != nil {
		return ctx, nil, err
	}
	service, releaseService, err := confine.ServeHostSocket(credentials.Socket())
	if err != nil {
		_ = credentials.Close()
		return ctx, nil, err
	}
	launchTC := tc
	launchTC.Files.SessionReadPaths = append(slices.Clone(tc.Files.SessionReadPaths), admitted.reads...)
	launchTC.Files.GrantedWriteRoots = append(slices.Clone(tc.Files.GrantedWriteRoots), admitted.writes...)
	plan, release, err := p.owners.Launch(ctx, launchTC, "git", p.commandLine(), []confine.HostService{service})
	if err != nil {
		releaseService()
		_ = credentials.Close()
		return ctx, nil, err
	}
	agent, hasAgent := grantedSSHAgent(ctx, tc)
	if hasAgent {
		// The launch dials the capability's endpoint, which differs from the
		// agent's own path where confinement relays sockets.
		reference, _ := confine.SocketEndpointReferences(agent.ApprovedPath)
		plan = plan.WithExtraEnv("SSH_AUTH_SOCK=" + reference)
	}
	credentials.Admit(admitted.urls...)
	done := func(failed bool) {
		outcome := tools.AgentLaunchOutcome{Failed: failed}
		if failed {
			outcome.Refusals = withheldAuthority(ctx, admitted.urls, credentials.Declined(), hasAgent)
		}
		release(context.WithoutCancel(ctx), outcome)
		releaseService()
		_ = credentials.Close()
	}
	return gitexec.WithAgentLaunch(ctx, gitexec.AgentLaunch{Plan: plan, Credentials: credentials}), done, nil
}

// withheldAuthority is what a failed network command lacked that a grant
// would supply: saved credentials Git asked for without the credential-store
// grant, and the user's SSH agent for an SSH remote without its socket
// capability. Each reads as a refusal the matching capability opens.
func withheldAuthority(ctx context.Context, urls, declined []string, hasAgent bool) []confine.SandboxRefusal {
	var refusals []confine.SandboxRefusal
	for _, recipient := range declined {
		refusals = append(refusals, confine.SandboxRefusal{Operation: "credential-read", Target: recipient, Process: "git", Count: 1, Recovery: confine.RecoverCredentialStore})
	}
	if agent := gitexec.HostSSHAgentSocket(ctx); agent != "" && !hasAgent && slices.ContainsFunc(urls, isSSHRemote) {
		refusals = append(refusals, confine.SandboxRefusal{Operation: "network-outbound", Target: agent, Process: "ssh", Count: 1, Recovery: confine.RecoverSocketPath, Grant: agent})
	}
	return refusals
}

func isSSHRemote(raw string) bool {
	lower := strings.ToLower(strings.TrimSpace(raw))
	return strings.HasPrefix(lower, "ssh://") || !strings.Contains(lower, "://") && remoteNetworkHost(raw) != ""
}

// remoteHelperURL is Git's <transport>::<address> form, which runs the
// git-remote-<transport> program.
var remoteHelperURL = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9+.-]*::`)

// admitLocalRepository binds a local remote through filesystem authority:
// the path the remote names and every directory Git opens through it, its
// git directory, common directory and object store, write for push, and the
// alternate object stores it only reads.
func admitLocalRepository(ctx context.Context, p Prepared, local string, push bool, admitted *admittedRemotes) error {
	bound, err := p.paths.Bind(ctx, local, push)
	if err != nil {
		return err
	}
	admit := func(path string, write bool) {
		if write {
			admitted.writes = append(admitted.writes, path)
		} else {
			admitted.reads = append(admitted.reads, path)
		}
	}
	admit(bound.Resolved.Abs, push)
	own, shared, ok := gitrepo.RepositoryDirs(bound.Resolved.Abs)
	if !ok {
		return nil
	}
	for _, group := range []struct {
		dirs  []string
		write bool
	}{{own, push}, {shared, false}} {
		for _, path := range group.dirs {
			if within(bound.Resolved.Abs, path) {
				continue
			}
			reached, err := p.paths.Bind(ctx, path, group.write)
			if err != nil {
				return err
			}
			admit(reached.Resolved.Abs, group.write)
		}
	}
	return nil
}

func within(root, path string) bool {
	rel, err := filepath.Rel(root, path)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// authorizeRemote admits one remote: network hosts through the egress gate,
// local repositories through filesystem authority, write for push.
func authorizeRemote(ctx context.Context, p Prepared, tc tools.ToolContext, dir, rawURL string, push bool, admitted *admittedRemotes, stderr io.Writer) error {
	rawURL = strings.TrimSpace(rawURL)
	if rawURL == "" {
		return nil
	}
	if err := gitargv.ValidateCloneURL(rawURL); err != nil {
		writeLine(stderr, fmt.Sprintf("fatal: %s: invalid remote '%s' [COMMAND_OPERAND_INVALID]", p.command.spec.Name, rawURL))
		return &toolrejection.ToolReject{Code: "COMMAND_OPERAND_INVALID", Data: map[string]any{"command": p.command.spec.Name, "word": rawURL, "expected": "a safe remote URL", "reason": "The remote is not a URL or path Git can fetch from safely."}}

	}
	if remoteHelperURL.MatchString(rawURL) {
		writeLine(stderr, fmt.Sprintf("fatal: %s: remote helper '%s' is not available [COMMAND_OPERAND_INVALID]", p.command.spec.Name, rawURL))
		return &toolrejection.ToolReject{Code: "COMMAND_OPERAND_INVALID", Data: map[string]any{"command": p.command.spec.Name, "word": rawURL, "expected": "a remote over https, http, ssh or git, or a local repository", "reason": "A remote helper runs a local program the URL names."}}
	}
	host := remoteNetworkHost(rawURL)
	if host == "" {
		local, ok := gitargv.LocalCloneSourcePath(rawURL)
		if !ok {
			writeLine(stderr, fmt.Sprintf("fatal: %s: invalid remote '%s' [COMMAND_OPERAND_INVALID]", p.command.spec.Name, rawURL))
			return &toolrejection.ToolReject{Code: "COMMAND_OPERAND_INVALID", Data: map[string]any{"command": p.command.spec.Name, "word": rawURL, "expected": "a local repository path or file:// URL", "reason": "Git cannot name a local repository from the remote."}}
		}
		if !filepath.IsAbs(local) {
			local = filepath.Join(dir, local)
		}
		return admitLocalRepository(ctx, p, local, push, admitted)
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
