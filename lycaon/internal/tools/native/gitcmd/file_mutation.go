package gitcmd

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/lycaon/lycaon/internal/commandprogram"
	"github.com/lycaon/lycaon/internal/git"
	"github.com/lycaon/lycaon/internal/gitrepo"
	"github.com/lycaon/lycaon/internal/toolcontract"
	"github.com/lycaon/lycaon/internal/toolrejection"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/tools/native"
)

type fileMutation struct {
	ctx          context.Context
	p            Prepared
	tc           tools.ToolContext
	dir          string
	repo         gitrepo.Repo
	out          *meteredOutput
	stderr       io.Writer
	index        []trackedEntry
	indexHash    string
	blobs        map[string][]byte
	headSnap     string
	headOID      string
	headEntries  map[string]trackedEntry
	status       int
	effects      bool
	indexUnknown bool
	indexChanged bool
}

func currentIndexHash(repo gitrepo.Repo) string {
	if shared, err := filepath.Glob(filepath.Join(repo.GitDir, "sharedindex.*")); err != nil || len(shared) > 0 {
		return ""
	}
	raw, err := os.ReadFile(filepath.Join(repo.GitDir, "index"))
	if err != nil {
		if os.IsNotExist(err) {
			return "absent"
		}
		return ""
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
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
	operands, err := fileOperands(ctx, p, dir, repo.Root)
	if err == nil {
		r.indexHash = currentIndexHash(repo)
		r.index, err = trackedEntries(ctx, p, repo.Root, operands, p.command.spec.Name == "git mv")
	}
	if err == nil {
		if p.command.spec.Name == "git rm" {
			err = r.remove()
		} else {
			err = r.move()
		}
	}
	certainty := toolcontract.NoEffect
	if r.effects || r.indexChanged {
		certainty = toolcontract.EffectCompleted
	}
	if r.indexUnknown {
		certainty = toolcontract.EffectUnknown
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
			if len(expected) > 0 && (r.indexHash == "" || r.indexChanged || currentIndexHash(r.repo) != r.indexHash) {
				paths := make([]string, 0, len(expected))
				for path := range expected {
					paths = append(paths, path)
				}
				current, err := trackedEntries(ctx, r.p, r.repo.Root, paths, true)
				if err != nil {
					return err
				}
				byPath := map[string]trackedEntry{}
				for _, entry := range current {
					byPath[entry.path] = entry
				}
				for path, entry := range expected {
					got := byPath[path]
					if got.mode != entry.mode || got.oid != entry.oid || got.unmerged != entry.unmerged {
						return &toolrejection.ToolReject{Code: "GIT_OPERATION_PRECONDITION", Data: map[string]any{"reason": "repository_changed_during_review", "path": path}}
					}
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

func (r *fileMutation) stage(entries []git.IndexEntry, expected map[string]trackedEntry, check func() error) error {
	if len(entries) == 0 {
		return nil
	}
	paths := make([]string, len(entries))
	for i, entry := range entries {
		paths[i] = entry.Path
	}
	result, err := (native.GitMutations{Git: r.p.owners.Git, Boundary: r.p.owners.Boundary}).Operate(r.ctx, r.reviewContext(expected, check), git.OperationRequest{Kind: "stage", Entries: entries, Paths: paths})
	if result.Attempted && !result.AfterObserved {
		r.indexUnknown = true
	}
	changed := result.Attempted && result.AfterObserved && result.Before.Index != result.After.Index
	r.indexChanged = r.indexChanged || changed
	if changed {
		recordStaged(r.p.owners, r.tc.ChatSessionID(), r.repo.Root, paths)
	}
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
