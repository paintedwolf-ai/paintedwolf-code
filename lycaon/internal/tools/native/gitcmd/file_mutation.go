package gitcmd

import (
	"context"
	"fmt"
	"io"

	"github.com/lycaon/lycaon/internal/commandprogram"
	"github.com/lycaon/lycaon/internal/git"
	"github.com/lycaon/lycaon/internal/gitrepo"
	"github.com/lycaon/lycaon/internal/toolcontract"
	"github.com/lycaon/lycaon/internal/toolrejection"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/tools/native"
)

type fileMutation struct {
	ctx     context.Context
	p       Prepared
	tc      tools.ToolContext
	dir     string
	repo    gitrepo.Repo
	out     *meteredOutput
	stderr  io.Writer
	index   []trackedEntry
	status  int
	effects bool
}

func removeOperands(spec toolcontract.CommandSpecification, bound commandprogram.BoundArgv) error {
	if len(bound.Operands()) == 0 {
		return operandInvalid(spec, spec.Name, "at least one PATH")
	}
	return nil
}
func moveOperands(spec toolcontract.CommandSpecification, bound commandprogram.BoundArgv) error {
	if len(bound.Operands()) < 2 {
		return operandInvalid(spec, spec.Name, "SOURCE ... DESTINATION")
	}
	return nil
}

func runFileMutation(ctx context.Context, p Prepared, tc tools.ToolContext, dir string, repo gitrepo.Repo, out *meteredOutput, stderr io.Writer) (toolcontract.ProducerResult, error) {
	if p.filesystem == nil {
		return settleMutationError(unsupportedFileForm(p, p.command.spec.Name, "shared filesystem operations"), toolcontract.NoEffect)
	}
	r := &fileMutation{ctx: ctx, p: p, tc: tc, dir: dir, repo: repo, out: out, stderr: stderr}
	var err error
	r.index, err = trackedEntries(ctx, p, repo.Root)
	if err == nil {
		if p.command.spec.Name == "git rm" {
			err = r.remove()
		} else {
			err = r.move()
		}
	}
	certainty := toolcontract.NoEffect
	if r.effects {
		certainty = toolcontract.EffectCompleted
	}
	if err == nil {
		err = out.err
	}
	if err != nil {
		return settleGitError(err, stderr, certainty)
	}
	return toolcontract.ProducerResult{Status: r.status, Terminal: toolcontract.ProducerCompleted, Complete: true, PublicationEligible: r.status == 0, EffectCertainty: certainty}, nil
}

func (r *fileMutation) fail(code int, format string, args ...any) {
	r.status = code
	writeLine(r.stderr, fmt.Sprintf(format, args...))
}

// The selected index records must remain the records used to compute the
// operation, including across the filesystem review and later index review.
func (r *fileMutation) reviewContext(expected map[string]trackedEntry, check func() error) tools.ToolContext {
	tc := r.tc
	tc.Files.FileChangeReview = func(ctx context.Context, changes []tools.FileChange) error {
		verify := func() error {
			current, err := trackedEntries(ctx, r.p, r.repo.Root)
			if err != nil {
				return err
			}
			byPath := map[string]trackedEntry{}
			for _, entry := range current {
				byPath[entry.path] = entry
			}
			for path, entry := range expected {
				got := byPath[path]
				if got.mode != entry.mode || got.oid != entry.oid {
					return &toolrejection.ToolReject{Code: "GIT_OPERATION_PRECONDITION", Data: map[string]any{"reason": "repository_changed_during_review", "path": path}}
				}
			}
			if check != nil {
				return check()
			}
			return nil
		}
		if err := verify(); err != nil {
			return err
		}
		if err := r.tc.ReviewFileChanges(ctx, changes...); err != nil {
			return err
		}
		return verify()
	}
	return tc
}

func (r *fileMutation) stage(entries []git.IndexEntry, expected map[string]trackedEntry) error {
	if len(entries) == 0 {
		return nil
	}
	paths := make([]string, len(entries))
	for i, entry := range entries {
		paths[i] = entry.Path
	}
	result, err := (native.GitMutations{Git: r.p.owners.Git, Boundary: r.p.owners.Boundary}).Operate(r.ctx, r.reviewContext(expected, nil), git.OperationRequest{Kind: "stage", Entries: entries, Paths: paths})
	r.effects = r.effects || result.Attempted
	if err != nil {
		return err
	}
	if result.Status == "failed" {
		r.fail(max(result.ExitCode, 1), "%s", result.Diagnostics)
		return nil
	}
	recordStaged(r.p.owners, r.tc.ChatSessionID(), r.repo.Root, paths)
	return nil
}
