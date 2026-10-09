package command

import (
	"context"
	"errors"
	"fmt"
	"github.com/lycaon/lycaon/internal/toolrejection"
	"io"
	"sort"
	"strings"

	"github.com/lycaon/lycaon/internal/commandsurface"
	"github.com/lycaon/lycaon/internal/exec"
	"github.com/lycaon/lycaon/internal/fseffect"
	"github.com/lycaon/lycaon/internal/hostcmd"
	"github.com/lycaon/lycaon/internal/sandbox"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/tools/projectpaths"
)

// OutputCommitter commits redirected stream output to durable storage.
type OutputCommitter func(ctx context.Context, tc tools.ToolContext, loc fseffect.Location, output io.Reader, appendMode bool) error

var defaultOutputCommitter OutputCommitter

// SetOutputCommitter registers the host write pipeline committer for redirected stream output.
func SetOutputCommitter(c OutputCommitter) {
	defaultOutputCommitter = c
}

// CommandIO resolves a plan's stream files with read/write scope checks.
// toolName is "command" or "verify" so redirect scope failures emit WRITE_SCOPE_DENIED.
func CommandIO(ctx context.Context, boundary *sandbox.Boundary, tctx tools.ToolContext, plan commandsurface.Plan, args map[string]any, toolName string) (hostcmd.IOParams, error) {
	var out hostcmd.IOParams
	binding := plan.IO

	if binding.Stdin != "" {
		if len(binding.Stdin) > exec.DefaultMaxStdinBytes {
			return out, fmt.Errorf("%w at %d bytes", exec.ErrStdinTooLarge, exec.DefaultMaxStdinBytes)
		}
		out.Stdin = &exec.StdinSpec{Literal: []byte(binding.Stdin)}
		out.StdinProvided = true
	}
	if binding.StdinFrom != "" {
		resolved, err := projectpaths.ResolveRead(ctx, boundary, tctx, binding.StdinFrom)
		if err != nil {
			return out, err
		}
		loc := resolved.EffectLocation()
		out.Stdin = &exec.StdinSpec{From: &loc}
		out.StdinFrom = resolved.DisplayPath
		out.StdinProvided = true
	}

	inlineEnv, err := parseEnvArg(args)
	if err != nil {
		return out, err
	}
	if len(inlineEnv) > 0 {
		if err := exec.ValidateInlineEnv(inlineEnv); err != nil {
			if errors.Is(err, exec.ErrInvalidEnvKey) || errors.Is(err, exec.ErrBlockedEnvKey) {
				return out, &toolrejection.ToolReject{Code: "ENV_KEY_INVALID", Data: map[string]any{"reason": err.Error()}}
			}
			return out, err
		}
		out.InlineEnv = inlineEnv
	}

	seenKeys := make(map[string]struct{})
	for k := range inlineEnv {
		seenKeys[k] = struct{}{}
	}
	for _, st := range plan.Stages {
		for k := range st.Env {
			seenKeys[k] = struct{}{}
		}
	}
	if len(seenKeys) > 0 {
		keys := make([]string, 0, len(seenKeys))
		for k := range seenKeys {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		out.EnvKeys = keys
	}

	redirect := &exec.RedirectSpec{
		Commit: func(commitCtx context.Context, loc fseffect.Location, output io.Reader, appendMode bool) error {
			return commitCommandOutput(commitCtx, tctx, loc, output, appendMode)
		},
	}
	if binding.StdoutTo != "" {
		target, display, err := resolveOutputTarget(ctx, boundary, tctx, binding.StdoutTo, toolName)
		if err != nil {
			return out, err
		}
		redirect.Stdout = &exec.OutputTarget{Location: target, Append: binding.Append}
		out.StdoutTo = display
	}
	if binding.StderrTo != "" {
		target, display, err := resolveOutputTarget(ctx, boundary, tctx, binding.StderrTo, toolName)
		if err != nil {
			return out, err
		}
		redirect.Stderr = &exec.OutputTarget{Location: target, Append: binding.Append}
		out.StderrTo = display
	}
	for _, write := range plan.InlineWrites() {
		target, _, err := resolveOutputTarget(ctx, boundary, tctx, write.Path, toolName)
		if err != nil {
			return out, err
		}
		redirect.Bind(write.Written, target)
	}
	for _, read := range plan.InlineReads() {
		resolved, err := projectpaths.ResolveRead(ctx, boundary, tctx, read.Path)
		if err != nil {
			return out, err
		}
		redirect.Bind(read.Written, resolved.EffectLocation())
	}
	if redirect.Stdout != nil || redirect.Stderr != nil || len(redirect.Files) > 0 {
		out.Redirect = redirect
	}
	return out, nil
}

// resolveOutputTarget applies the write door's scope to one output file.
func resolveOutputTarget(ctx context.Context, boundary *sandbox.Boundary, tctx tools.ToolContext, path, toolName string) (fseffect.Location, string, error) {
	resolved, err := projectpaths.ResolveWrite(ctx, boundary, tctx, path)
	if err != nil {
		return fseffect.Location{}, "", redirectWriteScopeReject(ctx, boundary, path, tctx.ProfileID(), toolName, err)
	}
	if err := assertProfileWriteScope(ctx, boundary, tctx, path, toolName); err != nil {
		return fseffect.Location{}, "", redirectWriteScopeReject(ctx, boundary, resolved.DisplayPath, tctx.ProfileID(), toolName, err)
	}
	return resolved.EffectLocation(), resolved.DisplayPath, nil
}

func parseEnvArg(args map[string]any) (map[string]string, error) {
	raw, ok := args["env"]
	if !ok || raw == nil {
		return nil, nil
	}
	obj, ok := raw.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("env must be an object")
	}
	if len(obj) == 0 {
		return nil, nil
	}
	out := make(map[string]string, len(obj))
	for k, v := range obj {
		s, ok := v.(string)
		if !ok {
			return nil, fmt.Errorf("env[%q] must be a string", k)
		}
		k = strings.TrimSpace(k)
		if k == "" {
			return nil, fmt.Errorf("env key is empty")
		}
		out[k] = s
	}
	return out, nil
}

func commitCommandOutput(ctx context.Context, tc tools.ToolContext, loc fseffect.Location, output io.Reader, appendMode bool) error {
	if defaultOutputCommitter == nil {
		return fmt.Errorf("output committer not configured")
	}
	return defaultOutputCommitter(ctx, tc, loc, output, appendMode)
}
