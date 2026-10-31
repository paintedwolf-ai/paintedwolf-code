package gitcmd

import (
	"context"
	"errors"
	"fmt"
	"io"
	"path"
	"path/filepath"
	"slices"
	"strconv"

	"github.com/lycaon/lycaon/internal/commandprogram"
	"github.com/lycaon/lycaon/internal/gitrepo"
	"github.com/lycaon/lycaon/internal/toolcontract"
	"github.com/lycaon/lycaon/internal/toolrejection"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/tools/native/mutation"
	"github.com/lycaon/lycaon/internal/tools/native/patchapply"
)

// applyInput is one patch source and the parsed sections it holds.
type applyInput struct {
	name string
	data []byte
}

// applySection is one file section bound to its paths and computed result.
type applySection struct {
	file  patchapply.File
	input applyInput
	// name is how Git names the file in messages: the new name, or the old
	// one for a deletion, after -p and --directory.
	name             string
	oldName, newName string
	oldRel, newRel   string
	target           mutation.PatchTarget
	before           []byte
	change           mutation.PatchResult
	// stages records a three-way conflict: base, ours and theirs.
	stages   *[3][]byte
	indexMod string
}

// applyRun is one git apply invocation.
type applyRun struct {
	ctx      context.Context
	p        Prepared
	tc       tools.ToolContext
	stderr   io.Writer
	dir      string
	repo     gitrepo.Repo
	cached   bool
	index    bool
	threeWay bool
	status   int
	effects  int
	// failures are the hunks that did not apply, for the receipt.
	failures []patchapply.Failure
}

// runApply is git apply over the shared hunk applier: every section is
// parsed, bound and computed before anything changes, and nothing changes
// unless every section applies (or --3way merges it). Work tree files land
// through the same pinned publication as patch; index changes are one
// reviewed Git operation with git add's session-staging rules.
func runApply(ctx context.Context, p Prepared, tc tools.ToolContext, streams commandprogram.Streams, out *meteredOutput, stderr io.Writer) (toolcontract.ProducerResult, error) {
	if p.filesystem == nil {
		return settleMutationError(toolrejection.RejectInvalidArguments("COMMAND_PLATFORM_UNAVAILABLE", map[string]any{"command": p.command.spec.Name, "capability": "the program's staged publication owner"}), toolcontract.NoEffect)
	}
	run := &applyRun{ctx: ctx, p: p, tc: tc, stderr: stderr, cached: has(p.Argv, "--cached"), threeWay: has(p.Argv, "-3")}
	run.index = has(p.Argv, "--index") || (run.threeWay && !run.cached)
	err := run.apply(streams.Input, out)
	if out.err != nil && err == nil {
		err = out.err
	}
	certainty := toolcontract.NoEffect
	if run.effects > 0 {
		certainty = toolcontract.EffectCompleted
	}
	if err != nil {
		return settleGitError(err, stderr, certainty)
	}
	result := toolcontract.ProducerResult{Status: run.status, Terminal: toolcontract.ProducerCompleted, Complete: true, PublicationEligible: run.status == 0, EffectCertainty: certainty}
	if len(run.failures) > 0 {
		result.Evidence = toolcontract.OperationEvidence{Kind: patchapply.EvidenceKind, EvidenceRef: patchapply.EvidenceRef(run.failures)}
	}
	return result, nil
}

func (r *applyRun) apply(stdin io.Reader, stdout io.Writer) error {
	dir, status, err := resolveCommandDir(r.ctx, r.p, r.stderr, false)
	if err != nil || status != 0 {
		r.status = status
		return err
	}
	r.dir = dir
	if r.cached || r.index {
		if _, r.repo, status, err = resolveCommandRepo(r.ctx, r.p, r.stderr, true); err != nil || status != 0 {
			r.status = status
			return err
		}
	}
	inputs, err := r.inputs(stdin)
	if err != nil {
		return err
	}
	sections, err := r.parse(inputs)
	if err != nil || r.status != 0 {
		return err
	}
	ws, status := checkWhitespace(r.p.Argv, sections, r.stderr)
	if r.status = status; status != 0 {
		return nil
	}
	if reportOnly(r.p.Argv) {
		writeReports(stdout, r.p.Argv, sections)
		if !has(r.p.Argv, "--check") {
			return nil
		}
	}
	if err := r.compute(sections); err != nil {
		return err
	}
	if r.status != 0 {
		return nil
	}
	if has(r.p.Argv, "--check") {
		ws.summarize(r.stderr)
		return nil
	}
	if err := r.publish(sections); err != nil {
		return err
	}
	ws.summarize(r.stderr)
	return nil
}

// reportOnly reports --stat, --numstat or --summary, which print instead of
// applying unless --check also checks.
func reportOnly(argv commandprogram.BoundArgv) bool {
	return has(argv, "--stat") || has(argv, "--numstat") || has(argv, "--summary")
}

// inputs reads each PATCH operand as a pinned source, or standard input.
func (r *applyRun) inputs(stdin io.Reader) ([]applyInput, error) {
	operands := r.p.Argv.Operands().Values()
	if len(operands) == 0 {
		operands = []string{"-"}
	}
	execution := r.p.execution
	inputs := make([]applyInput, 0, len(operands))
	for _, operand := range operands {
		if operand == "-" {
			data, err := readBounded(stdin, r.p.owners.limit("stream_bytes"))
			if err != nil {
				return nil, err
			}
			inputs = append(inputs, applyInput{name: "<stdin>", data: data})
			continue
		}
		bound, err := r.p.paths.Bind(r.ctx, r.join(operand), false)
		if err != nil {
			return nil, err
		}
		data, err := r.p.filesystem.CapturePatchInput(r.ctx, r.tc, execution, bound.Resolved.Abs)
		if err != nil {
			return nil, err
		}
		inputs = append(inputs, applyInput{name: operand, data: data})
	}
	return inputs, nil
}

func readBounded(reader io.Reader, maximum int64) ([]byte, error) {
	if reader == nil {
		return nil, nil
	}
	data, err := io.ReadAll(io.LimitReader(reader, maximum+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > maximum {
		return nil, &toolrejection.ToolReject{Code: "COMMAND_LIMIT_EXCEEDED", Data: map[string]any{"limit_id": "stream_bytes", "resource_unit": "bytes", "maximum": maximum, "observed": int64(len(data))}}
	}
	return data, nil
}

func (r *applyRun) join(name string) string {
	if filepath.IsAbs(name) {
		return name
	}
	return filepath.Join(r.dir, filepath.FromSlash(name))
}

// parse reads every input and binds each section's names: -p N components
// removed (default 1), -R applied, --directory prepended, and --include and
// --exclude deciding in command-line order.
func (r *applyRun) parse(inputs []applyInput) ([]*applySection, error) {
	strip := 1
	if raw, ok := optionValue(r.p.Argv, "-p"); ok {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 0 {
			return nil, operandInvalid(r.p.command.spec, raw, "a non-negative number for -p")
		}
		strip = n
	}
	prefix, _ := optionValue(r.p.Argv, "--directory")
	var sections []*applySection
	for _, input := range inputs {
		parsed, err := patchapply.Parse(input.data)
		if errors.Is(err, patchapply.ErrNoPatch) {
			writeLine(r.stderr, `error: No valid patches in input (allow with "--allow-empty")`)
			r.status = 128
			return nil, nil
		}
		if err != nil {
			writeLine(r.stderr, fmt.Sprintf("error: corrupt patch in %s: %v", input.name, err))
			r.status = 128
			return nil, nil
		}
		for _, file := range parsed.Files {
			if file.Symlink() {
				return nil, &toolrejection.ToolReject{Code: "COMMAND_NATIVE_FORM_UNSUPPORTED", Data: map[string]any{
					"command": r.p.command.spec.Name, "word": "symbolic link patch", "expected": "a regular file patch", "expected_form": r.p.command.spec.Argv}}
			}
			if has(r.p.Argv, "-R") {
				name := file.NewName
				if file, err = patchapply.Reverse(file); err != nil {
					writeLine(r.stderr, "error: cannot reverse-apply a binary patch without the reverse hunk to '"+name+"'")
					r.status = 1
					//nolint:nilerr // Reverse binary patch failure reports status 1 on stderr.
					return nil, nil
				}
			}
			oldName, newName, err := file.Names(strip)
			if err != nil {
				return nil, operandInvalid(r.p.command.spec, file.NewName, "a name with enough components for -p")
			}
			if prefix != "" {
				for _, name := range []*string{&oldName, &newName} {
					if *name != "" {
						*name = path.Join(prefix, *name)
					}
				}
			}
			section := &applySection{file: file, input: input, name: newName, oldName: oldName, newName: newName}
			if newName == "" {
				section.name = oldName
			}
			if r.included(section.name) {
				sections = append(sections, section)
			}
		}
	}
	if has(r.p.Argv, "-R") {
		// Undoing a series undoes its last change first, as Git orders it.
		slices.Reverse(sections)
	}
	return sections, nil
}

// included applies --include and --exclude in the order given; with any
// --include, a path no pattern matches is left out.
func (r *applyRun) included(name string) bool {
	anyInclude := false
	for _, item := range r.p.Argv.Items {
		if item.Option == nil || (item.Option.Key != "--include" && item.Option.Key != "--exclude") || len(item.Option.Values) == 0 {
			continue
		}
		include := item.Option.Key == "--include"
		anyInclude = anyInclude || include
		if wildmatch(item.Option.Values[0].Value, name) {
			return include
		}
	}
	return !anyInclude
}
