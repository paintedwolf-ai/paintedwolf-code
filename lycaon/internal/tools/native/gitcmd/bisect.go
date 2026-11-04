package gitcmd

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/lycaon/lycaon/internal/commandprogram"
	"github.com/lycaon/lycaon/internal/git"
	"github.com/lycaon/lycaon/internal/gitrepo"
	"github.com/lycaon/lycaon/internal/toolcontract"
	"github.com/lycaon/lycaon/internal/toolrejection"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/tools/native"
)

// bisectReads change neither the work tree nor the session.
var bisectReads = map[string]bool{"git bisect log": true, "git bisect terms": true, "git bisect visualize": true, "git bisect view": true}

func bisectOperands(spec toolcontract.CommandSpecification, bound commandprogram.BoundArgv) error {
	operands := bound.Operands().Values()
	switch spec.Name {
	case "git bisect":
		if len(operands) == 0 {
			return operandInvalid(spec, spec.Name, "a TERM such as good or bad")
		}
		if operands[0] == "replay" {
			return &toolrejection.ToolReject{Code: "COMMAND_NATIVE_FORM_UNSUPPORTED", Data: map[string]any{"command": "git bisect", "word": "replay", "expected": "git bisect log as the session record; replay from a file is not supported", "expected_form": spec.Argv}}
		}
	case "git bisect run":
		if len(operands) == 0 {
			return operandInvalid(spec, spec.Name, "a check COMMAND")
		}
	case "git bisect reset":
		if len(operands) > 1 {
			return operandInvalid(spec, operands[1], "at most one COMMIT")
		}
	case "git bisect visualize", "git bisect view":
		if !has(bound, "--oneline") {
			return operandInvalid(spec, spec.Name, "--oneline; graphical and paged views are not supported")
		}
		return noOperands(spec, bound)
	case "git bisect log", "git bisect terms":
		return noOperands(spec, bound)
	}
	return nil
}

type bisect struct {
	ctx     context.Context
	p       Prepared
	tc      tools.ToolContext
	root    string
	out     *meteredOutput
	stderr  io.Writer
	state   git.BisectState
	effects bool
}

func runBisect(ctx context.Context, p Prepared, tc tools.ToolContext, repo gitrepo.Repo, out *meteredOutput, stderr io.Writer) (toolcontract.ProducerResult, error) {
	b := &bisect{ctx: ctx, p: p, tc: tc, root: repo.Root, out: out, stderr: stderr}
	var err error
	if b.state, err = git.ReadBisect(ctx, b.root); err != nil {
		return settleGitError(err, stderr, toolcontract.NoEffect)
	}
	operands := p.Argv.Operands().Values()
	var status int
	switch p.command.spec.Name {
	case "git bisect start":
		status, err = b.start()
	case "git bisect":
		status, err = b.verdictCommand(operands)
	case "git bisect skip":
		status, err = b.verdictCommand(append([]string{"skip"}, operands...))
	case "git bisect run":
		return b.run(p.Argv.Operands())
	case "git bisect reset":
		status, err = b.reset(firstOrEmpty(operands))
	case "git bisect log":
		status, err = b.log()
	case "git bisect terms":
		status, err = b.terms()
	default:
		status, err = b.visualize()
	}
	return b.settle(status, err)
}

func (b *bisect) settle(status int, err error) (toolcontract.ProducerResult, error) {
	certainty := toolcontract.NoEffect
	if b.effects {
		certainty = toolcontract.EffectCompleted
	}
	if err == nil {
		err = b.out.err
	}
	if err != nil {
		return settleGitError(err, b.stderr, certainty)
	}
	return toolcontract.ProducerResult{Status: status, Terminal: toolcontract.ProducerCompleted, Complete: true, PublicationEligible: status == 0, EffectCertainty: certainty}, nil
}

func (b *bisect) notBisecting() int {
	_, _ = io.WriteString(b.stderr, "You need to start by \"git bisect start\"\n\n")
	return 1
}

// start refuses a tree with tracked changes, because every step checks out
// another commit over it. A separate worktree keeps the user's tree as is.
func (b *bisect) start() (int, error) {
	status, err := b.p.owners.Git.Status(b.ctx, b.root)
	if err != nil {
		return 0, err
	}
	for _, entry := range status.Files {
		if entry.Status != "??" {
			return 0, &toolrejection.ToolReject{Code: "GIT_BISECT_DIRTY_TREE", Data: map[string]any{"command": "git bisect start", "path": entry.Path}}
		}
	}
	terms := [2]string{"bad", "good"}
	var revs, names, words []string
	for _, item := range b.p.Argv.Items {
		if item.Option != nil {
			value := ""
			if len(item.Option.Values) > 0 {
				value = item.Option.Values[0].Value
			}
			words = append(words, item.Option.Spelling+"="+value)
			switch item.Option.Key {
			case "--term-new":
				terms[0] = value
			case "--term-old":
				terms[1] = value
			}
		}
	}
	revs = b.p.command.beforeDash
	names = b.p.command.afterDash
	words = append(words, revs...)
	if b.p.Argv.EndOfOptions {
		words = append(append(words, "--"), names...)
	}
	start := b.state.Start
	if !b.state.Active {
		if start, err = git.BisectStart(b.ctx, b.root); err != nil {
			return 0, err
		}
	}
	oids := make([]string, len(revs))
	for i, rev := range revs {
		oid, err := git.ResolveBisectCommit(b.ctx, b.root, rev)
		if err != nil {
			return 0, err
		}
		if oid == "" {
			writeLine(b.stderr, fmt.Sprintf("fatal: '%s' does not appear to be a valid revision", rev))
			return 128, nil
		}
		oids[i] = oid
	}
	if b.state.Active {
		if err := git.ClearBisect(b.ctx, b.root); err != nil {
			return 0, err
		}
	}
	b.state = git.BisectState{Active: true, Start: start, New: terms[0], Old: terms[1]}
	pathspec := ""
	if len(names) > 0 {
		pathspec = " " + git.QuoteBisectWords(append([]string{"--"}, names...))
	}
	b.state.Names = pathspec
	files := map[string]string{"BISECT_START": start + "\n", "BISECT_TERMS": terms[0] + "\n" + terms[1] + "\n", "BISECT_NAMES": pathspec + "\n", "BISECT_LOG": ""}
	refs := map[string]string{}
	var log strings.Builder
	for i, oid := range oids {
		term := terms[1]
		if i == 0 {
			term = terms[0]
			b.state.Bad = oid
			refs["refs/bisect/"+term] = oid
		} else {
			b.state.Good = append(b.state.Good, oid)
			refs["refs/bisect/"+term+"-"+oid] = oid
		}
		if err := b.comment(&log, term, oid); err != nil {
			return 0, err
		}
	}
	log.WriteString("git bisect start")
	if len(words) > 0 {
		log.WriteString(" " + git.QuoteBisectWords(words))
	}
	log.WriteString("\n")
	if err := git.WriteBisect(b.ctx, b.root, files, log.String(), refs); err != nil {
		return 0, err
	}
	b.effects = true
	return b.next()
}

func (b *bisect) comment(log *strings.Builder, label, oid string) error {
	subject, err := git.CommitSubject(b.ctx, b.root, oid)
	if err != nil {
		return err
	}
	fmt.Fprintf(log, "# %s: [%s] %s\n", label, oid, subject)
	return nil
}

// verdictCommand records TERM [REV...] for the current terms, or skip.
func (b *bisect) verdictCommand(words []string) (int, error) {
	if !b.state.Active {
		return b.notBisecting(), nil
	}
	term, revs := words[0], words[1:]
	switch {
	case term == "skip":
	case term == b.state.New || (term == "bad" && b.state.Old == "good") || (term == "new" && b.state.Old == "old"):
		term = b.state.New
		if len(revs) > 1 {
			writeLine(b.stderr, fmt.Sprintf("fatal: 'git bisect %s' can take only one argument.", term))
			return 128, nil
		}
	case term == b.state.Old || (term == "good" && b.state.New == "bad") || (term == "old" && b.state.New == "new"):
		term = b.state.Old
	default:
		writeLine(b.stderr, fmt.Sprintf("fatal: unknown command: '%s'", term))
		return 129, nil
	}
	if len(revs) == 0 {
		revs = []string{"HEAD"}
	}
	for _, rev := range revs {
		oid, err := git.ResolveBisectCommit(b.ctx, b.root, rev)
		if err != nil {
			return 0, err
		}
		if oid == "" {
			writeLine(b.stderr, fmt.Sprintf("fatal: Bad rev input: %s", rev))
			return 128, nil
		}
		if err := b.record(term, oid); err != nil {
			return 0, err
		}
	}
	return b.next()
}

func (b *bisect) record(term, oid string) error {
	var log strings.Builder
	if err := b.comment(&log, term, oid); err != nil {
		return err
	}
	fmt.Fprintf(&log, "git bisect %s %s\n", term, oid)
	ref := "refs/bisect/" + term + "-" + oid
	switch term {
	case b.state.New:
		ref = "refs/bisect/" + term
		b.state.Bad = oid
	case b.state.Old:
		b.state.Good = append(b.state.Good, oid)
	default:
		b.state.Skip = append(b.state.Skip, oid)
	}
	b.effects = true
	return git.WriteBisect(b.ctx, b.root, nil, log.String(), map[string]string{ref: oid})
}

// next reports the session's next state and checks out the next commit
// through the Git owner's checkout operation. Status 2 means only skipped
// commits remain.
func (b *bisect) next() (int, error) {
	status, _, err := b.advance()
	return status, err
}

// advance is next, also reporting whether the first new commit is found.
func (b *bisect) advance() (int, bool, error) {
	step, err := git.NextBisect(b.ctx, b.root, b.state)
	if err != nil {
		return 0, false, err
	}
	if step.First != "" {
		return 0, true, b.first(step.First)
	}
	status, err := b.step(step)
	return status, false, err
}

func (b *bisect) step(step git.BisectStep) (int, error) {
	switch {
	case step.Waiting != "":
		writeLine(b.out, step.Waiting)
		return 0, git.WriteBisect(b.ctx, b.root, nil, "# "+step.Waiting+"\n", nil)
	case step.Next == "":
		var log strings.Builder
		log.WriteString("# only skipped commits left to test\n")
		fmt.Fprintf(b.out, "There are only 'skip'ped commits left to test.\nThe first %s commit could be any of:\n", b.state.New)
		for _, oid := range step.Candidates {
			fmt.Fprintf(b.out, "%s\n", oid)
			if err := b.comment(&log, "possible first "+b.state.New+" commit", oid); err != nil {
				return 0, err
			}
		}
		writeLine(b.out, "We cannot bisect more!")
		return 2, git.WriteBisect(b.ctx, b.root, nil, log.String(), nil)
	}
	if err := git.WriteBisect(b.ctx, b.root, map[string]string{"BISECT_ANCESTORS_OK": "", "BISECT_EXPECTED_REV": step.Next + "\n"}, "", nil); err != nil {
		return 0, err
	}
	if status, err := b.checkout(git.OperationRequest{Kind: "checkout", Action: "detach", Ref: step.Next}); err != nil || status != 0 {
		return status, err
	}
	subject, err := git.CommitSubject(b.ctx, b.root, step.Next)
	if err != nil {
		return 0, err
	}
	fmt.Fprintf(b.out, "Bisecting: %d %s left to test after this (roughly %d %s)\n[%s] %s\n", step.Remaining, plural(step.Remaining, "revision"), step.Steps, plural(step.Steps, "step"), step.Next, subject)
	return 0, nil
}

func plural(n int, word string) string {
	if n == 1 {
		return word
	}
	return word + "s"
}

func (b *bisect) first(oid string) error {
	var log strings.Builder
	if err := b.comment(&log, "first "+b.state.New+" commit", oid); err != nil {
		return err
	}
	fmt.Fprintf(b.out, "%s is the first %s commit\n", oid, b.state.New)
	if _, _, err := (git.CommandReader{}).ReadCommand(b.ctx, b.root, []string{"show", "--no-color", "--stat", "--format=medium", oid, "--"}, nil, b.out); err != nil {
		return err
	}
	return git.WriteBisect(b.ctx, b.root, nil, log.String(), nil)
}

func (b *bisect) checkout(req git.OperationRequest) (int, error) {
	res, err := (native.GitMutations{Git: b.p.owners.Git, Boundary: b.p.owners.Boundary}).Operate(b.ctx, b.tc, req)
	if res.Attempted {
		b.effects = true
	}
	if err != nil {
		return 0, err
	}
	if diagnostics := strings.TrimSpace(res.Diagnostics); diagnostics != "" && res.Status != "no_op" {
		writeLine(b.stderr, diagnostics)
	}
	if res.Status == "failed" || res.Status == "conflicts" {
		return max(res.ExitCode, 1), nil
	}
	return 0, nil
}

// reset returns to the session's start, or to COMMIT, then clears the
// session.
func (b *bisect) reset(commit string) (int, error) {
	if !b.state.Active {
		writeLine(b.out, "We are not bisecting.")
		return 0, nil
	}
	target := b.state.Start
	if commit != "" {
		target = commit
	}
	req := git.OperationRequest{Kind: "checkout", Action: "detach", Ref: target}
	if entries, err := b.p.owners.Git.RevParse(b.ctx, b.root, []string{"refs/heads/" + target}); err == nil && len(entries) > 0 {
		req = git.OperationRequest{Kind: "checkout", Branch: target}
	}
	if status, err := b.checkout(req); err != nil || status != 0 {
		return status, err
	}
	b.effects = true
	return 0, git.ClearBisect(b.ctx, b.root)
}

func (b *bisect) log() (int, error) {
	if !b.state.Active {
		writeLine(b.stderr, "error: We are not bisecting.")
		return 1, nil
	}
	_, err := io.WriteString(b.out, b.state.Log)
	return 0, err
}

func (b *bisect) terms() (int, error) {
	if !b.state.Active {
		writeLine(b.stderr, "error: no terms defined")
		return 1, nil
	}
	switch {
	case has(b.p.Argv, "--term-old"):
		writeLine(b.out, b.state.Old)
	case has(b.p.Argv, "--term-new"):
		writeLine(b.out, b.state.New)
	default:
		fmt.Fprintf(b.out, "Your current terms are %s for the old state\nand %s for the new state.\n", b.state.Old, b.state.New)
	}
	return 0, nil
}

func (b *bisect) visualize() (int, error) {
	if !b.state.Active || b.state.Bad == "" {
		return 1, nil
	}
	args := []string{"log", "--oneline", "--no-color", b.state.Bad, "--not"}
	args = append(args, b.state.Good...)
	if b.state.Names != "" {
		args = append(args, strings.Fields(strings.ReplaceAll(b.state.Names, "'", ""))...)
	}
	diagnostics, status, err := (git.CommandReader{}).ReadCommand(b.ctx, b.root, args, nil, b.out)
	if err == nil {
		_, err = b.stderr.Write(diagnostics)
	}
	return status, err
}

// run tests each commit with CMD through normal dispatch: status 0 is old,
// 125 skips, 1 to 127 is new, and anything else stops the session where it
// is. An interruption leaves the session recorded at the commit under test.
func (b *bisect) run(argv commandprogram.Fields) (toolcontract.ProducerResult, error) {
	if !b.state.Active {
		return b.settle(b.notBisecting(), nil)
	}
	if b.p.nested == nil {
		return b.settle(0, &toolrejection.ToolReject{Code: "COMMAND_NATIVE_FORM_UNSUPPORTED", Data: map[string]any{"command": "git bisect run", "word": "run", "expected": "a program that can dispatch the check command"}})
	}
	if b.state.Bad == "" || len(b.state.Good) == 0 {
		status, err := b.next()
		if err == nil && status == 0 {
			writeLine(b.stderr, "error: bisect run failed: no good and bad commits known")
			status = 1
		}
		return b.settle(status, err)
	}
	words := make([]string, len(argv))
	for i, field := range argv {
		words[i] = field.Value
	}
	environment := map[string]string{}
	for _, entry := range b.p.request.Environment {
		if name, value, ok := strings.Cut(entry, "="); ok {
			environment[name] = value
		}
	}
	for {
		head, err := git.ResolveBisectCommit(b.ctx, b.root, "HEAD")
		if err != nil {
			return b.settle(0, err)
		}
		fmt.Fprintf(b.out, "running %s\n", git.QuoteBisectWords(words))
		result, err := b.p.nested.RunNested(b.ctx, b.p.request, argv, environment, commandprogram.Streams{Input: commandprogram.NullInput(), Output: b.out, Error: b.stderr})
		if err == nil {
			err = b.ctx.Err()
		}
		if err != nil || result.Terminal == toolcontract.ProducerRefused || result.Terminal == toolcontract.ProducerInterrupted {
			return b.stopped(head, result, err)
		}
		term := b.state.New
		switch {
		case result.Status == 0:
			term = b.state.Old
		case result.Status == 125:
			term = "skip"
		case result.Status < 0 || result.Status >= 128:
			writeLine(b.stderr, fmt.Sprintf("error: bisect run failed: exit code %d from %s is < 0 or >= 128", result.Status, git.QuoteBisectWords(words)))
			return b.settle(1, nil)
		}
		if err := b.record(term, head); err != nil {
			return b.settle(0, err)
		}
		status, found, err := b.advance()
		switch {
		case err != nil:
			return b.settle(0, err)
		case status == 2:
			writeLine(b.stderr, "error: bisect run cannot continue any more")
			return b.settle(2, nil)
		case status != 0:
			return b.settle(status, nil)
		case found:
			writeLine(b.out, "bisect found first "+b.state.New+" commit")
			return b.settle(0, nil)
		}
	}
}

// stopped keeps the recorded session and says how to leave it.
func (b *bisect) stopped(head string, result toolcontract.ProducerResult, err error) (toolcontract.ProducerResult, error) {
	start := b.state.Start
	writeLine(b.stderr, fmt.Sprintf("git bisect run stopped while testing %s; the repository is still bisecting from '%s'. Run git bisect reset to return to it.", head, start))
	if err == nil {
		err = &toolrejection.ToolReject{Code: result.StopCode, Data: map[string]any{"command": "git bisect run"}}
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return toolcontract.ProducerResult{Status: 130, Terminal: toolcontract.ProducerInterrupted, EffectCertainty: toolcontract.EffectCompleted}, err
	}
	return settleMutationError(err, toolcontract.EffectCompleted)
}
