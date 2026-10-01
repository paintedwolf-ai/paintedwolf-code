package git

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/lycaon/lycaon/internal/gitexec"
)

const MaxOperationCopyBytes int64 = 1 << 30

type operationPlan struct {
	request  OperationRequest
	args     []string
	stage    []string
	refs     map[string]string
	opts     gitexec.Opts
	stashOID string
}

func planOperation(ctx context.Context, dir string, req OperationRequest) (operationPlan, error) {
	p := operationPlan{request: req, refs: map[string]string{}, opts: hermeticOpts(2 * time.Minute)}
	identity := commitIdentity(ctx, dir)
	identity.Timestamp = time.Now().UTC().Truncate(time.Second)
	p.opts.Identity = &identity
	// Disable implicit effects that the structured request did not name.
	p.opts.ExtraConfig = []string{"submodule.recurse=false", "merge.autoStash=false", "merge.log=false", "merge.branchdesc=false", "commit.cleanup=strip", "rerere.enabled=false", "core.splitIndex=false", "filter.lfs.smudge=", "filter.lfs.process=", "filter.lfs.required=false"}
	var err error
	switch req.Kind {
	case "checkout":
		err = p.checkout(ctx, dir)
	case "merge":
		err = p.merge(ctx, dir)
	case "stash":
		err = p.stash(ctx, dir)
	default:
		err = operationRefusal("unknown_operation")
	}
	return p, err
}

func (p *operationPlan) pin(ctx context.Context, dir, ref string) (string, error) {
	oid, err := resolveCommit(ctx, dir, ref)
	if err == nil {
		p.refs[ref] = oid
	}
	return oid, err
}

func (p *operationPlan) checkout(ctx context.Context, dir string) error {
	r := p.request
	if r.Branch == "" {
		return operationRefusal("branch_required")
	}
	if err := validateGitRef(r.Branch, "branch"); err != nil {
		return err
	}
	if _, err := restoreGit(ctx, dir, hermeticOpts(0), "check-ref-format", "refs/heads/"+r.Branch); err != nil {
		return err
	}
	ref := "refs/heads/" + r.Branch
	oid, err := optionalObject(ctx, dir, ref)
	if err != nil {
		return err
	}
	p.refs[ref] = oid
	if r.Create {
		if oid != "" {
			return operationRefusal("branch_exists")
		}
		start := r.Ref
		if start == "" {
			start = "HEAD"
		}
		oid, err = p.pin(ctx, dir, start)
		if err != nil {
			return err
		}
		p.args = []string{"checkout", "--no-recurse-submodules", "--no-track", "-b", r.Branch, oid, "--"}
	} else {
		if oid == "" {
			return operationRefusal("branch_missing")
		}
		p.args = []string{"checkout", "--no-recurse-submodules", r.Branch, "--"}
	}
	return nil
}

func (p *operationPlan) merge(ctx context.Context, dir string) error {
	r := p.request
	switch r.Action {
	case "", "start":
		source, err := p.pin(ctx, dir, r.Ref)
		if err != nil {
			return err
		}
		mode := r.Mode
		if mode == "" {
			mode = "ff"
		}
		flags := map[string]string{"ff": "--ff", "ff_only": "--ff-only", "no_ff": "--no-ff"}
		flag, ok := flags[mode]
		if !ok {
			return operationRefusal("invalid_merge_mode")
		}
		message := r.Message
		if message == "" {
			message = "Merge " + r.Ref
		}
		p.args = []string{"merge", flag, "--no-edit", "--no-autostash"}
		if r.NoCommit {
			p.args = append(p.args, "--no-commit")
		}
		p.args = append(p.args, "-m", message, "--end-of-options", source)
	case "continue":
		p.stage = r.Paths
		p.args = []string{"commit", "--no-edit"}
		if r.Message != "" {
			p.args = append(p.args, "-m", r.Message)
		}
	case "abort":
		p.args = []string{"merge", "--abort"}
	default:
		return operationRefusal("invalid_merge_action")
	}
	return nil
}

func (p *operationPlan) stash(ctx context.Context, dir string) error {
	r := p.request
	switch r.Action {
	case "save":
		if len(r.Paths) == 0 {
			return operationRefusal("explicit_paths_required")
		}
		if len(r.Paths) > DefaultGitCommitMaxPaths {
			return operationRefusal("explicit_path_limit")
		}
		for _, path := range r.Paths {
			if path == "" || filepath.Clean(path) == "." || filepath.IsAbs(path) || filepath.Clean(path) == ".." || strings.HasPrefix(filepath.Clean(path), ".."+string(filepath.Separator)) {
				return operationRefusal("explicit_repository_paths_required", path)
			}
		}
	case "apply", "drop":
		if r.Ref == "" {
			return operationRefusal("stash_ref_required")
		}
		oid, err := p.pin(ctx, dir, r.Ref)
		if err != nil {
			return err
		}
		p.stashOID = oid
		// Stashes have an index parent. Ordinary commits are not stash entries.
		if _, err = resolveCommit(ctx, dir, oid+"^2"); err != nil {
			return operationRefusal("invalid_stash")
		}
		p.args = []string{"stash", r.Action}
		if r.Action == "apply" {
			// Status reflects the live index, including unrelated staging.
			p.args = append(p.args, "--quiet")
			if r.ReinstateIndex {
				p.args = append(p.args, "--index")
			}
			p.args = append(p.args, oid)
		} else {
			selector, err := stashSelector(ctx, dir, oid, r.Ref)
			if err != nil {
				return err
			}
			p.args = append(p.args, selector)
		}
	default:
		return operationRefusal("invalid_stash_action")
	}
	return nil
}

func (p *operationPlan) run(ctx context.Context, dir string) ([]byte, int, error) {
	if p.request.Kind == "stash" && p.request.Action != "drop" {
		return p.runStash(ctx, dir)
	}
	if len(p.stage) > 0 {
		args := append([]string{"--literal-pathspecs", "add", "--"}, p.stage...)
		out, code, err := gitexec.Run(ctx, dir, args, p.opts)
		if err != nil || code != 0 {
			return out, code, err
		}
	}
	return gitexec.Run(ctx, dir, p.args, p.opts)
}

func (p *operationPlan) validateRefs(ctx context.Context, dir string) error {
	for ref, expected := range p.refs {
		var current string
		var err error
		if strings.HasPrefix(ref, "refs/heads/") {
			current, err = optionalObject(ctx, dir, ref)
		} else {
			current, err = resolveCommit(ctx, dir, ref)
		}
		if err != nil || current != expected {
			return operationRefusal("repository_changed_during_review")
		}
	}
	if p.request.Kind == "stash" && p.request.Action == "drop" {
		selector, err := stashSelector(ctx, dir, p.stashOID, p.request.Ref)
		if err != nil || selector != p.args[len(p.args)-1] {
			return operationRefusal("stash_changed_during_review")
		}
	}
	return nil
}

func stashSelector(ctx context.Context, dir, oid, requested string) (string, error) {
	entries, err := readStashes(ctx, dir)
	if err != nil {
		return "", err
	}
	for _, entry := range entries {
		if entry.OID == oid && entry.Ref == requested {
			return entry.Ref, nil
		}
	}
	selector := ""
	for _, entry := range entries {
		if entry.OID != oid {
			continue
		}
		if requested == entry.Ref {
			return entry.Ref, nil
		}
		if selector != "" {
			return "", operationRefusal("ambiguous_stash_identity")
		}
		selector = entry.Ref
	}
	if selector == "" {
		return "", operationRefusal("stash_not_in_reflog")
	}
	return selector, nil
}

// StashEntry keeps an immutable object identity beside the transient reflog selector.
type StashEntry struct {
	Ref     string `json:"ref"`
	OID     string `json:"oid"`
	Subject string `json:"subject"`
}

func (m *Manager) ListStashes(ctx context.Context, dir string, offset, limit int) ([]StashEntry, error) {
	dir, err := repositoryProjectDir(dir)
	if err != nil {
		return nil, err
	}
	if offset < 0 || limit < 1 {
		return nil, operationRefusal("invalid_stash_page")
	}
	return readStashes(ctx, dir, fmt.Sprintf("--skip=%d", offset), fmt.Sprintf("--max-count=%d", limit))
}

// readStashes with no window reads the whole stash reflog, so selector checks see every entry.
func readStashes(ctx context.Context, dir string, window ...string) ([]StashEntry, error) {
	entries := []StashEntry{}
	oid, err := optionalObject(ctx, dir, "refs/stash")
	if err != nil || oid == "" {
		return entries, err
	}
	args := append([]string{"reflog", "show", "--format=%H%x00%gd%x00%gs", "-z"}, window...)
	args = append(args, "refs/stash")
	var fields []string
	err = gitexec.RunRecords(ctx, dir, args, hermeticOpts(0), func(raw []byte) error {
		fields = append(fields, string(raw))
		if len(fields) < 3 {
			return nil
		}
		if !validObjectID(fields[0]) {
			return fmt.Errorf("invalid stash identity")
		}
		entries = append(entries, StashEntry{OID: fields[0], Ref: fields[1], Subject: fields[2]})
		fields = fields[:0]
		return nil
	})
	if err != nil {
		return nil, err
	}
	if len(fields) != 0 {
		return nil, fmt.Errorf("malformed stash records")
	}
	return entries, nil
}
