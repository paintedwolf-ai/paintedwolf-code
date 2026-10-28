package gitcmd

import (
	"context"
	"io"
	"path/filepath"

	"github.com/lycaon/lycaon/internal/toolcontract"
)

func runInit(ctx context.Context, p Prepared, out *meteredOutput, stderr io.Writer) (toolcontract.ProducerResult, error) {
	dir, status, err := resolveCommandDir(ctx, p, stderr, false)
	if err != nil || status != 0 {
		return settle(status, err, p.command.spec.Name)
	}
	target := dir
	args := p.Argv.Operands().Values()
	if len(args) == 1 {
		target = args[0]
		if !filepath.IsAbs(target) {
			target = filepath.Join(dir, target)
		}
	}
	bound, err := p.paths.Bind(ctx, target, true)
	if err != nil {
		return settleMutationError(err, toolcontract.NoEffect)
	}
	options := []string{}
	if has(p.Argv, "-q") {
		options = append(options, "-q")
	}
	if branch, ok := optionValue(p.Argv, "-b"); ok {
		options = append(options, "-b", branch)
	}
	if has(p.Argv, "--bare") {
		options = append(options, "--bare")
	}
	gitTarget := ""
	if len(args) == 1 {
		gitTarget = bound.Resolved.Abs
	}
	diagnostics, code, err := p.owners.Git.InitCommand(ctx, dir, gitTarget, p.command.config, options, out)
	if out.err != nil {
		return settleMutationError(out.err, toolcontract.EffectUnknown)
	}
	if len(diagnostics) > 0 {
		_, _ = stderr.Write(diagnostics)
	}
	if err != nil {
		return settleGitError(err, stderr, toolcontract.EffectUnknown)
	}
	if code != 0 {
		return toolcontract.ProducerResult{Status: code, Terminal: toolcontract.ProducerCompleted, Complete: true, EffectCertainty: toolcontract.EffectUnknown}, nil
	}
	return toolcontract.ProducerResult{Status: 0, Terminal: toolcontract.ProducerCompleted, Complete: true, PublicationEligible: true, EffectCertainty: toolcontract.EffectCompleted}, nil
}
