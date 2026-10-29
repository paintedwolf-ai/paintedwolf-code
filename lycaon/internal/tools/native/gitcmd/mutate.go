package gitcmd

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/lycaon/lycaon/internal/commandprogram"
	"github.com/lycaon/lycaon/internal/git"
	"github.com/lycaon/lycaon/internal/gitrepo"
	"github.com/lycaon/lycaon/internal/toolcontract"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/tools/native"
)

func invokeMutation(ctx context.Context, p Prepared, tc tools.ToolContext, streams commandprogram.Streams) (toolcontract.ProducerResult, error) {
	stderr := streams.Error
	if stderr == nil {
		stderr = io.Discard
	}
	out := &meteredOutput{ctx: ctx, w: streams.Output, maximum: p.owners.limit("stream_bytes")}
	if out.w == nil {
		out.w = io.Discard
	}
	spec := p.command.spec
	switch spec.Name {
	case "git stash":
		if len(p.command.beforeDash) > 0 && p.command.beforeDash[0] == "show" {
			status, err := p.run(ctx, streams)
			return settle(status, err, spec.Name)
		}
	case "git clean":
		if has(p.Argv, "-n") {
			status, err := p.run(ctx, streams)
			return settle(status, err, spec.Name)
		}
	case "git init":
		return runInit(ctx, p, out, stderr)
	case "git clone":
		return runClone(ctx, p, tc, out, stderr)
	case "git apply":
		return runApply(ctx, p, tc, streams, out, stderr)
	}
	write := spec.Name != "git ls-remote" && !bisectReads[spec.Name]
	dir, repo, status, err := resolveCommandRepo(ctx, p, stderr, write)
	if err != nil || status != 0 {
		return settle(status, err, spec.Name)
	}
	if write {
		if _, err := p.paths.Bind(ctx, repo.Root, true); err != nil {
			return settleMutationError(err, toolcontract.NoEffect)
		}
	}
	scopedTC := withRepoRoot(tc, repo.Root)
	switch spec.Name {
	case "git rm", "git mv":
		return runFileMutation(ctx, p, scopedTC, dir, repo, out, stderr)
	case "git add":
		return runAdd(ctx, p, scopedTC, dir, repo, out, stderr)
	case "git commit":
		return runCommit(ctx, p, scopedTC, dir, repo, out, stderr)
	case "git restore":
		return runRestore(ctx, p, scopedTC, dir, repo, stderr)
	case "git checkout":
		return runCheckout(ctx, p, scopedTC, dir, repo, out, stderr)
	case "git switch":
		return runSwitch(ctx, p, scopedTC, repo, out, stderr)
	case "git merge":
		return runMerge(ctx, p, scopedTC, repo, out, stderr)
	case "git cherry-pick":
		return runSequence(ctx, p, scopedTC, "cherry-pick", out, stderr)
	case "git revert":
		return runSequence(ctx, p, scopedTC, "revert", out, stderr)
	case "git rebase":
		return runRebase(ctx, p, scopedTC, out, stderr)
	case "git stash":
		return runStash(ctx, p, scopedTC, dir, repo, out, stderr)
	case "git reset":
		return runReset(ctx, p, scopedTC, dir, repo, out, stderr)
	case "git clean":
		return runClean(ctx, p, scopedTC, dir, repo, out, stderr)
	case "git worktree":
		return runWorktree(ctx, p, dir, repo, stderr)
	case "git fetch":
		return runNetwork(ctx, p, scopedTC, dir, repo, out, stderr, toolcontract.EffectCompleted)
	case "git push":
		return runNetwork(ctx, p, scopedTC, dir, repo, out, stderr, toolcontract.EffectCompleted)
	case "git ls-remote":
		return runNetwork(ctx, p, scopedTC, dir, repo, out, stderr, toolcontract.NoEffect)
	case "git pull":
		return runPull(ctx, p, scopedTC, dir, repo, out, stderr)
	case "git bisect", "git bisect start", "git bisect skip", "git bisect run", "git bisect reset",
		"git bisect log", "git bisect terms", "git bisect visualize", "git bisect view":
		return runBisect(ctx, p, scopedTC, repo, out, stderr)
	default:
		return settleMutationError(fmt.Errorf("unhandled git command %s", spec.Name), toolcontract.NoEffect)
	}
}

func runAdd(ctx context.Context, p Prepared, tc tools.ToolContext, dir string, repo gitrepo.Repo, out *meteredOutput, stderr io.Writer) (toolcontract.ProducerResult, error) {
	mode := ""
	if has(p.Argv, "-A") {
		mode = "all"
	} else if has(p.Argv, "-u") {
		mode = "update"
	}
	paths, err := repoRelPaths(ctx, p, dir, repo.Root, p.Argv.Operands().Values())
	if err != nil {
		return settleMutationError(err, toolcontract.NoEffect)
	}
	mut := native.GitMutations{Git: p.owners.Git, Boundary: p.owners.Boundary}
	result, err := mut.Operate(ctx, tc, git.OperationRequest{Kind: "add", Mode: mode, Paths: paths})
	if err == nil {
		recordStaged(p.owners, tc.ChatSessionID(), repo.Root, append(append([]string{}, result.Paths...), paths...))
	}
	return settleOperate(p, result, out, stderr, has(p.Argv, "-q"), err)
}

func runCommit(ctx context.Context, p Prepared, tc tools.ToolContext, dir string, repo gitrepo.Repo, out *meteredOutput, stderr io.Writer) (toolcontract.ProducerResult, error) {
	var parts []string
	for _, opt := range p.Argv.Options("-m") {
		for _, v := range opt.Values {
			if s := strings.TrimSpace(v.Value); s != "" {
				parts = append(parts, s)
			}
		}
	}
	msg := strings.Join(parts, "\n\n")
	mut := native.GitMutations{Git: p.owners.Git, Boundary: p.owners.Boundary}
	if !has(p.Argv, "--amend") && len(p.Argv.Operands()) == 0 && !has(p.Argv, "-a") {
		if _, err := os.Stat(filepath.Join(repo.GitDir, "MERGE_HEAD")); err == nil {
			res, opErr := mut.Operate(ctx, tc, git.OperationRequest{Kind: "merge", Action: "continue", Message: msg})
			if opErr == nil && res.Status == "completed" {
				clearStaged(p.owners, tc.ChatSessionID(), repo.Root)
			}
			return settleOperate(p, res, out, stderr, has(p.Argv, "-q"), opErr)
		}
		if seq := activeSequence(repo.GitDir); seq != "" {
			res, opErr := mut.Operate(ctx, tc, git.OperationRequest{Kind: seq, Action: "continue"})
			if opErr == nil && res.Status == "completed" {
				clearStaged(p.owners, tc.ChatSessionID(), repo.Root)
			}
			return settleOperate(p, res, out, stderr, has(p.Argv, "-q"), opErr)
		}
	}
	if msg == "" {
		if !has(p.Argv, "--amend") {
			return settleMutationError(operandInvalid(p.command.spec, "--no-edit", "--amend or an active merge/sequence"), toolcontract.NoEffect)
		}
		var buf bytes.Buffer
		if _, code, err := (git.CommandReader{}).ReadCommand(ctx, repo.Root, []string{"log", "-1", "--format=%B", "HEAD"}, nil, &buf); err != nil || code != 0 {
			return settleGitError(fmt.Errorf("cannot read HEAD commit message"), stderr, toolcontract.NoEffect)
		}
		msg = strings.TrimSpace(buf.String())
	}
	paths, err := repoRelPaths(ctx, p, dir, repo.Root, p.Argv.Operands().Values())
	if err != nil {
		return settleMutationError(err, toolcontract.NoEffect)
	}
	staged := sessionStaged(p.owners, tc.ChatSessionID(), repo.Root)
	if has(p.Argv, "-a") {
		if st, sErr := p.owners.Git.Status(ctx, repo.Root); sErr == nil {
			for _, f := range st.Files {
				if len(f.Status) >= 2 && f.Status[1] != ' ' && f.Status[1] != '?' && f.Status[1] != '!' {
					staged = append(staged, f.Path)
				}
			}
		}
	}
	hash, staging, err := mut.Commit(ctx, tc, native.GitCommitRequest{Message: msg, Paths: paths, Staged: staged, Amend: has(p.Argv, "--amend"),
		IdentityName: p.command.configValue("user.name"), IdentityEmail: p.command.configValue("user.email")})
	if err != nil {
		var cf *git.CommitFailed
		if errors.As(err, &cf) {
			writeLine(stderr, cf.Output)
			code := max(cf.ExitCode, 1)
			certainty := toolcontract.NoEffect
			if len(staging.Paths) > 0 {
				certainty = toolcontract.EffectCompleted
			}
			return toolcontract.ProducerResult{Status: code, Terminal: toolcontract.ProducerCompleted, Complete: true, EffectCertainty: certainty}, nil
		}
		return settleGitError(err, stderr, toolcontract.NoEffect)
	}
	unstageRecorded(p.owners, tc.ChatSessionID(), repo.Root, staging.Paths)
	if !has(p.Argv, "-q") {
		branch, _ := p.owners.Git.Branch(ctx, repo.Root)
		if branch == "" {
			branch = "detached HEAD"
		}
		subject, _, _ := strings.Cut(msg, "\n")
		_, _ = fmt.Fprintf(out, "[%s %s] %s\n", branch, shortSHA(hash), subject)
		if out.err != nil {
			return settleMutationError(out.err, toolcontract.EffectCompleted)
		}
	}
	return toolcontract.ProducerResult{Status: 0, Terminal: toolcontract.ProducerCompleted, Complete: true, PublicationEligible: true, EffectCertainty: toolcontract.EffectCompleted}, nil
}

func runRestore(ctx context.Context, p Prepared, tc tools.ToolContext, dir string, repo gitrepo.Repo, stderr io.Writer) (toolcontract.ProducerResult, error) {
	source, _ := optionValue(p.Argv, "-s")
	staged := has(p.Argv, "-S")
	worktree := has(p.Argv, "-W")
	paths, err := expandRestorePaths(ctx, p, dir, repo.Root, p.Argv.Operands().Values(), source)
	if err != nil {
		return settleMutationError(err, toolcontract.NoEffect)
	}
	if len(paths) == 0 {
		return toolcontract.ProducerResult{Status: 0, Terminal: toolcontract.ProducerCompleted, Complete: true, PublicationEligible: true, EffectCertainty: toolcontract.NoEffect}, nil
	}
	mut := native.GitMutations{Git: p.owners.Git, Boundary: p.owners.Boundary}
	restored, err := mut.Restore(ctx, tc, git.GitRestoreOpts{Paths: paths, Source: source, Staged: staged, Worktree: worktree})
	if err != nil {
		return settleRestoreError(err, stderr)
	}
	if staged {
		unstageRecorded(p.owners, tc.ChatSessionID(), repo.Root, restored)
	}
	return toolcontract.ProducerResult{Status: 0, Terminal: toolcontract.ProducerCompleted, Complete: true, PublicationEligible: true, EffectCertainty: toolcontract.EffectCompleted}, nil
}

func runCheckout(ctx context.Context, p Prepared, tc tools.ToolContext, dir string, repo gitrepo.Repo, out *meteredOutput, stderr io.Writer) (toolcontract.ProducerResult, error) {
	before, after := p.command.beforeDash, p.command.afterDash
	var req git.OperationRequest
	switch {
	case has(p.Argv, "-b"):
		branch, _ := optionValue(p.Argv, "-b")
		req = git.OperationRequest{Kind: "checkout", Branch: branch, Create: true, Ref: firstOrEmpty(before)}
	case has(p.Argv, "--detach"):
		ref := firstOrEmpty(before)
		if ref == "" {
			ref = "HEAD"
		}
		req = git.OperationRequest{Kind: "checkout", Action: "detach", Ref: ref}
	case p.Argv.EndOfOptions && len(after) > 0:
		if len(before) > 1 {
			return settleMutationError(operandInvalid(p.command.spec, before[1], "at most one REF before --"), toolcontract.NoEffect)
		}
		paths, err := repoRelPaths(ctx, p, dir, repo.Root, after)
		if err != nil {
			return settleMutationError(err, toolcontract.NoEffect)
		}
		req = git.OperationRequest{Kind: "checkout", Action: "paths", Ref: firstOrEmpty(before), Paths: paths}
	case len(before) == 1 && before[0] == ".":
		req = git.OperationRequest{Kind: "checkout", Action: "paths", Paths: []string{"."}}
	case len(before) == 1:
		target := before[0]
		if entries, err := p.owners.Git.RevParse(ctx, repo.Root, []string{"refs/heads/" + target}); err == nil && len(entries) > 0 {
			req = git.OperationRequest{Kind: "checkout", Branch: target}
		} else if ok, _ := p.owners.Git.CommitExists(ctx, repo.Root, target); ok {
			req = git.OperationRequest{Kind: "checkout", Action: "detach", Ref: target}
		} else {
			req = git.OperationRequest{Kind: "checkout", Branch: target}
		}
	default:
		return settleMutationError(operandInvalid(p.command.spec, before[1], "-- before PATH operands"), toolcontract.NoEffect)
	}
	mut := native.GitMutations{Git: p.owners.Git, Boundary: p.owners.Boundary}
	res, err := mut.Operate(ctx, tc, req)
	return settleOperate(p, res, out, stderr, has(p.Argv, "-q"), err)
}

func runSwitch(ctx context.Context, p Prepared, tc tools.ToolContext, repo gitrepo.Repo, out *meteredOutput, stderr io.Writer) (toolcontract.ProducerResult, error) {
	ops := p.Argv.Operands().Values()
	var req git.OperationRequest
	switch {
	case has(p.Argv, "-c"):
		branch, _ := optionValue(p.Argv, "-c")
		req = git.OperationRequest{Kind: "checkout", Branch: branch, Create: true, Ref: firstOrEmpty(ops)}
	case has(p.Argv, "--detach"):
		ref := firstOrEmpty(ops)
		if ref == "" {
			ref = "HEAD"
		}
		req = git.OperationRequest{Kind: "checkout", Action: "detach", Ref: ref}
	default:
		req = git.OperationRequest{Kind: "checkout", Branch: ops[0]}
	}
	mut := native.GitMutations{Git: p.owners.Git, Boundary: p.owners.Boundary}
	res, err := mut.Operate(ctx, tc, req)
	return settleOperate(p, res, out, stderr, has(p.Argv, "-q"), err)
}

func runMerge(ctx context.Context, p Prepared, tc tools.ToolContext, repo gitrepo.Repo, out *meteredOutput, stderr io.Writer) (toolcontract.ProducerResult, error) {
	action := ""
	if has(p.Argv, "--continue") {
		action = "continue"
	} else if has(p.Argv, "--abort") {
		action = "abort"
	}
	mode := ""
	switch {
	case has(p.Argv, "--ff"):
		mode = "ff"
	case has(p.Argv, "--no-ff"):
		mode = "no_ff"
	case has(p.Argv, "--ff-only"):
		mode = "ff_only"
	}
	msg, _ := optionValue(p.Argv, "-m")
	req := git.OperationRequest{Kind: "merge", Action: action, Mode: mode, NoCommit: has(p.Argv, "--no-commit"), Message: msg, Ref: firstOrEmpty(p.Argv.Operands().Values())}
	mut := native.GitMutations{Git: p.owners.Git, Boundary: p.owners.Boundary}
	res, err := mut.Operate(ctx, tc, req)
	return settleOperate(p, res, out, stderr, has(p.Argv, "-q"), err)
}
