package gitcmd

import (
	"context"
	"fmt"
	"io"
	"path/filepath"
	"strings"

	"github.com/lycaon/lycaon/internal/git"
	"github.com/lycaon/lycaon/internal/gitrepo"
	"github.com/lycaon/lycaon/internal/toolcontract"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/tools/native"
)

func runStash(ctx context.Context, p Prepared, tc tools.ToolContext, dir string, repo gitrepo.Repo, out *meteredOutput, stderr io.Writer) (toolcontract.ProducerResult, error) {
	before, after := p.command.beforeDash, p.command.afterDash
	sub := "push"
	rest := before
	if len(before) > 0 {
		switch before[0] {
		case "push", "save", "apply", "pop", "drop":
			sub = before[0]
			rest = before[1:]
		}
	}
	mut := native.GitMutations{Git: p.owners.Git, Boundary: p.owners.Boundary}
	quiet := has(p.Argv, "-q")
	switch sub {
	case "apply", "drop":
		id := firstOrDefault(rest, "stash@{0}")
		res, err := mut.Operate(ctx, tc, git.OperationRequest{Kind: "stash", Action: sub, Ref: id, ReinstateIndex: has(p.Argv, "--index")})
		if err == nil && !quiet && sub == "drop" && res.Status == "completed" && strings.TrimSpace(res.Diagnostics) == "" {
			_, _ = fmt.Fprintf(out, "Dropped %s (%s)\n", id, res.Before.Stash)
		}
		return settleOperate(p, res, out, stderr, quiet, err)
	case "pop":
		id := firstOrDefault(rest, "stash@{0}")
		applied, err := mut.Operate(ctx, tc, git.OperationRequest{Kind: "stash", Action: "apply", Ref: id, ReinstateIndex: has(p.Argv, "--index")})
		if err != nil || applied.Status != "completed" && applied.Status != "no_op" {
			return settleOperate(p, applied, out, stderr, quiet, err)
		}
		dropped, dropErr := mut.Operate(ctx, tc, git.OperationRequest{Kind: "stash", Action: "drop", Ref: id})
		if dropErr != nil {
			return settleGitError(dropErr, stderr, toolcontract.EffectCompleted)
		}
		if !quiet && strings.TrimSpace(dropped.Diagnostics) == "" {
			_, _ = fmt.Fprintf(out, "Dropped %s (%s)\n", id, applied.Before.Stash)
		}
		return settleOperate(p, applied, out, stderr, quiet, nil)
	default:
		includeUntracked := has(p.Argv, "-u")
		rawPaths := append(append([]string{}, rest...), after...)
		paths, err := expandStashPaths(ctx, p, dir, repo.Root, rawPaths, includeUntracked)
		if err != nil {
			return settleMutationError(err, toolcontract.NoEffect)
		}
		if len(paths) == 0 {
			if !quiet {
				_, _ = io.WriteString(out, "No local changes to save\n")
			}
			return toolcontract.ProducerResult{Status: 0, Terminal: toolcontract.ProducerCompleted, Complete: true, PublicationEligible: true, EffectCertainty: toolcontract.NoEffect}, nil
		}
		msg, _ := optionValue(p.Argv, "-m")
		res, err := mut.Operate(ctx, tc, git.OperationRequest{Kind: "stash", Action: "save", Message: msg, IncludeUntracked: includeUntracked, Paths: paths})
		if err == nil && res.Status == "completed" {
			unstageRecorded(p.owners, tc.ChatSessionID(), repo.Root, paths)
			if !quiet && strings.TrimSpace(res.Diagnostics) == "" {
				_, _ = fmt.Fprintf(out, "Saved working directory and index state (%s)\n", shortSHA(res.After.Stash))
			}
		}
		return settleOperate(p, res, out, stderr, quiet, err)
	}
}

func runReset(ctx context.Context, p Prepared, tc tools.ToolContext, dir string, repo gitrepo.Repo, out *meteredOutput, stderr io.Writer) (toolcontract.ProducerResult, error) {
	mode := ""
	for _, m := range []string{"soft", "mixed", "hard", "keep", "merge"} {
		if has(p.Argv, "--"+m) {
			mode = m
			break
		}
	}
	before, after := p.command.beforeDash, p.command.afterDash
	var ref string
	var rawPaths []string
	switch {
	case p.Argv.EndOfOptions:
		if len(before) > 1 {
			return settleMutationError(operandInvalid(p.command.spec, before[1], "at most one REF before --"), toolcontract.NoEffect)
		}
		ref = firstOrEmpty(before)
		rawPaths = after
	case len(before) == 0:
		ref = "HEAD"
	case mode == "soft" || mode == "hard" || mode == "keep" || mode == "merge":
		if len(before) > 1 {
			return settleMutationError(operandInvalid(p.command.spec, before[1], "at most one REF with --"+mode), toolcontract.NoEffect)
		}
		ref = before[0]
	case len(before) == 1:
		if ok, _ := p.owners.Git.CommitExists(ctx, repo.Root, before[0]); ok {
			ref = before[0]
		} else {
			rawPaths = before
		}
	default:
		rawPaths = before
	}
	if len(rawPaths) > 0 && mode != "" && mode != "mixed" {
		return settleMutationError(operandInvalid(p.command.spec, rawPaths[0], "cannot combine --"+mode+" with paths"), toolcontract.NoEffect)
	}
	paths, err := repoRelPaths(ctx, p, dir, repo.Root, rawPaths)
	if err != nil {
		return settleMutationError(err, toolcontract.NoEffect)
	}
	mut := native.GitMutations{Git: p.owners.Git, Boundary: p.owners.Boundary}
	res, err := mut.Operate(ctx, tc, git.OperationRequest{Kind: "reset", Mode: mode, Ref: ref, Paths: paths})
	if err == nil {
		if len(paths) > 0 {
			unstageRecorded(p.owners, tc.ChatSessionID(), repo.Root, paths)
		} else if mode != "soft" {
			clearStaged(p.owners, tc.ChatSessionID(), repo.Root)
		}
	}
	return settleOperate(p, res, out, stderr, has(p.Argv, "-q"), err)
}

func runClean(ctx context.Context, p Prepared, tc tools.ToolContext, dir string, repo gitrepo.Repo, out *meteredOutput, stderr io.Writer) (toolcontract.ProducerResult, error) {
	mode := ""
	if has(p.Argv, "-d") {
		mode = "dirs"
	}
	paths, err := repoRelPaths(ctx, p, dir, repo.Root, p.Argv.Operands().Values())
	if err != nil {
		return settleMutationError(err, toolcontract.NoEffect)
	}
	mut := native.GitMutations{Git: p.owners.Git, Boundary: p.owners.Boundary}
	res, err := mut.Operate(ctx, tc, git.OperationRequest{Kind: "clean", Mode: mode, Paths: paths})
	return settleOperate(p, res, out, stderr, has(p.Argv, "-q"), err)
}

func runWorktree(ctx context.Context, p Prepared, dir string, repo gitrepo.Repo, stderr io.Writer) (toolcontract.ProducerResult, error) {
	ops := p.Argv.Operands().Values()
	switch ops[0] {
	case "add":
		branch, _ := optionValue(p.Argv, "-b")
		target := ops[1]
		if !filepath.IsAbs(target) {
			target = filepath.Join(dir, target)
		}
		bound, err := p.paths.Bind(ctx, target, true)
		if err != nil {
			return settleMutationError(err, toolcontract.NoEffect)
		}
		baseRef := ""
		if len(ops) == 3 {
			baseRef = ops[2]
		}
		if err := p.owners.Git.AddWorktree(ctx, repo.Root, bound.Resolved.Abs, branch, baseRef); err != nil {
			return settleGitError(err, stderr, toolcontract.NoEffect)
		}
	case "remove":
		target := ops[1]
		if !filepath.IsAbs(target) {
			target = filepath.Join(dir, target)
		}
		bound, err := p.paths.Bind(ctx, target, true)
		if err != nil {
			return settleMutationError(err, toolcontract.NoEffect)
		}
		if err := p.owners.Git.RemoveWorktree(ctx, repo.Root, bound.Resolved.Abs); err != nil {
			return settleGitError(err, stderr, toolcontract.NoEffect)
		}
	case "prune":
		if err := p.owners.Git.PruneWorktrees(ctx, repo.Root); err != nil {
			return settleGitError(err, stderr, toolcontract.NoEffect)
		}
	}
	return toolcontract.ProducerResult{Status: 0, Terminal: toolcontract.ProducerCompleted, Complete: true, PublicationEligible: true, EffectCertainty: toolcontract.EffectCompleted}, nil
}

func runSequence(ctx context.Context, p Prepared, tc tools.ToolContext, kind string, out *meteredOutput, stderr io.Writer) (toolcontract.ProducerResult, error) {
	action := sequenceAction(p.Argv)
	req := git.OperationRequest{Kind: kind, Action: action, NoCommit: has(p.Argv, "-n"), Refs: p.Argv.Operands().Values()}
	mut := native.GitMutations{Git: p.owners.Git, Boundary: p.owners.Boundary}
	res, err := mut.Operate(ctx, tc, req)
	return settleOperate(p, res, out, stderr, false, err)
}

func runRebase(ctx context.Context, p Prepared, tc tools.ToolContext, out *meteredOutput, stderr io.Writer) (toolcontract.ProducerResult, error) {
	action := sequenceAction(p.Argv)
	req := git.OperationRequest{Kind: "rebase", Action: action, Ref: firstOrEmpty(p.Argv.Operands().Values())}
	mut := native.GitMutations{Git: p.owners.Git, Boundary: p.owners.Boundary}
	res, err := mut.Operate(ctx, tc, req)
	return settleOperate(p, res, out, stderr, false, err)
}
