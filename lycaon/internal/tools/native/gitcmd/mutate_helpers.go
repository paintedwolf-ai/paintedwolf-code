package gitcmd

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/lycaon/lycaon/internal/commandprogram"
	"github.com/lycaon/lycaon/internal/git"
	"github.com/lycaon/lycaon/internal/toolcontract"
	"github.com/lycaon/lycaon/internal/toolrejection"
	"github.com/lycaon/lycaon/internal/tools/projectpaths"
)

func settleOperate(p Prepared, res git.OperationResult, out *meteredOutput, stderr io.Writer, quiet bool, err error) (toolcontract.ProducerResult, error) {
	if err != nil {
		return settleGitError(err, stderr, toolcontract.NoEffect)
	}
	diag := strings.TrimSpace(res.Diagnostics)
	diagnostics := io.Writer(out)
	if p.command.spec.Name == "git add" {
		diagnostics = stderr
	}
	switch res.Status {
	case "conflicts":
		writeLine(stderr, diag)
		code := max(res.ExitCode, 1)
		return toolcontract.ProducerResult{Status: code, Terminal: toolcontract.ProducerCompleted, Complete: true, EffectCertainty: toolcontract.EffectCompleted}, nil
	case "failed":
		writeLine(stderr, diag)
		code := max(res.ExitCode, 1)
		return toolcontract.ProducerResult{Status: code, Terminal: toolcontract.ProducerCompleted, Complete: true, EffectCertainty: toolcontract.NoEffect}, nil
	case "no_op":
		if !quiet && diag != "" {
			writeLine(diagnostics, diag)
		}
		if out.err != nil {
			return settleMutationError(out.err, toolcontract.NoEffect)
		}
		return toolcontract.ProducerResult{Status: 0, Terminal: toolcontract.ProducerCompleted, Complete: true, PublicationEligible: true, EffectCertainty: toolcontract.NoEffect}, nil
	default:
		if !quiet && diag != "" {
			writeLine(diagnostics, diag)
		}
		if out.err != nil {
			return settleMutationError(out.err, toolcontract.EffectCompleted)
		}
		return toolcontract.ProducerResult{Status: 0, Terminal: toolcontract.ProducerCompleted, Complete: true, PublicationEligible: true, EffectCertainty: toolcontract.EffectCompleted}, nil
	}
}

func settleRestoreError(err error, stderr io.Writer) (toolcontract.ProducerResult, error) {
	var precondition *git.OperationError
	if errors.As(err, &precondition) {
		return settleMutationError(&toolrejection.ToolReject{
			Code: "GIT_OPERATION_PRECONDITION",
			Data: map[string]any{"reason": precondition.Reason, "paths": precondition.Paths, "tool": "git_restore"},
		}, toolcontract.NoEffect)
	}
	return settleGitError(err, stderr, toolcontract.NoEffect)
}

func settleGitError(err error, stderr io.Writer, certainty toolcontract.EffectCertainty) (toolcontract.ProducerResult, error) {
	if toolrejection.AsToolReject(err) != nil || toolrejection.HostRefusal(err) != nil || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return settleMutationError(err, certainty)
	}
	var pathErr *projectpaths.PathError
	if errors.As(err, &pathErr) {
		return settleMutationError(err, certainty)
	}
	writeLine(stderr, "fatal: "+err.Error())
	return toolcontract.ProducerResult{Status: 128, Terminal: toolcontract.ProducerCompleted, Complete: true, EffectCertainty: certainty}, nil
}

func settleMutationError(err error, certainty toolcontract.EffectCertainty) (toolcontract.ProducerResult, error) {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		if certainty == toolcontract.NoEffect {
			certainty = toolcontract.EffectUnknown
		}
		return toolcontract.ProducerResult{Status: 130, Terminal: toolcontract.ProducerInterrupted, EffectCertainty: certainty}, err
	}
	if reject := toolrejection.AsToolReject(err); reject != nil {
		return toolcontract.ProducerResult{Status: 2, Terminal: toolcontract.ProducerRefused, StopCode: reject.Code, EffectCertainty: certainty}, err
	}
	var pathErr *projectpaths.PathError
	if errors.As(err, &pathErr) {
		return toolcontract.ProducerResult{Status: 2, Terminal: toolcontract.ProducerRefused, StopCode: pathErr.Code, EffectCertainty: certainty}, err
	}
	return toolcontract.ProducerResult{Status: 1, Terminal: toolcontract.ProducerFailed, EffectCertainty: certainty}, err
}

func stageKey(sessionID, repoRoot string) string {
	return sessionID + "\x00" + filepath.Clean(repoRoot)
}

func recordStaged(o *Owners, sessionID, repoRoot string, paths []string) {
	o.mu.Lock()
	defer o.mu.Unlock()
	key := stageKey(sessionID, repoRoot)
	set := o.staged[key]
	if set == nil {
		set = map[string]bool{}
		o.staged[key] = set
	}
	for _, p := range paths {
		p = filepath.ToSlash(p)
		if p != "" && p != "." {
			set[p] = true
		}
	}
}

func unstageRecorded(o *Owners, sessionID, repoRoot string, paths []string) {
	o.mu.Lock()
	defer o.mu.Unlock()
	set := o.staged[stageKey(sessionID, repoRoot)]
	if set == nil {
		return
	}
	for _, p := range paths {
		p = filepath.ToSlash(p)
		if p == "." {
			clear(set)
			return
		}
		delete(set, p)
	}
}

func clearStaged(o *Owners, sessionID, repoRoot string) {
	o.mu.Lock()
	defer o.mu.Unlock()
	delete(o.staged, stageKey(sessionID, repoRoot))
}

func sessionStaged(o *Owners, sessionID, repoRoot string) []string {
	o.mu.Lock()
	defer o.mu.Unlock()
	set := o.staged[stageKey(sessionID, repoRoot)]
	if len(set) == 0 {
		return nil
	}
	out := make([]string, 0, len(set))
	for p := range set {
		out = append(out, p)
	}
	sort.Strings(out)
	return out
}

func activeSequence(gitDir string) string {
	for _, dir := range []string{"rebase-merge", "rebase-apply"} {
		if _, err := os.Stat(filepath.Join(gitDir, dir)); err == nil {
			return "rebase"
		}
	}
	if _, err := os.Stat(filepath.Join(gitDir, "CHERRY_PICK_HEAD")); err == nil {
		return "cherry-pick"
	}
	if _, err := os.Stat(filepath.Join(gitDir, "REVERT_HEAD")); err == nil {
		return "revert"
	}
	return ""
}

func optionValue(bound commandprogram.BoundArgv, key string) (string, bool) {
	opt, ok := bound.Last(key)
	if !ok || len(opt.Values) == 0 {
		return "", false
	}
	return opt.Values[0].Value, true
}

func countFlags(bound commandprogram.BoundArgv, flags ...string) int {
	n := 0
	for _, f := range flags {
		if has(bound, f) {
			n++
		}
	}
	return n
}

func sequenceAction(bound commandprogram.BoundArgv) string {
	for _, act := range []string{"continue", "abort", "skip"} {
		if has(bound, "--"+act) {
			return act
		}
	}
	return ""
}

func firstOrEmpty(items []string) string {
	if len(items) == 0 {
		return ""
	}
	return items[0]
}

func firstOrDefault(items []string, def string) string {
	if len(items) == 0 || strings.TrimSpace(items[0]) == "" {
		return def
	}
	return items[0]
}

func shortSHA(sha string) string {
	if len(sha) > 7 {
		return sha[:7]
	}
	return sha
}

func writeLine(w io.Writer, text string) {
	text = strings.TrimRight(text, "\n")
	if text == "" {
		return
	}
	_, _ = io.WriteString(w, text+"\n")
}
