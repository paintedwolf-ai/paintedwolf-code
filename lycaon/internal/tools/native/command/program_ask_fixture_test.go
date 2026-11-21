package command_test

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/lycaon/lycaon/internal/commandprogram"
	lycexec "github.com/lycaon/lycaon/internal/exec"
	"github.com/lycaon/lycaon/internal/git"
	"github.com/lycaon/lycaon/internal/gitexec"
	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/layeredfs/privategit"
	"github.com/lycaon/lycaon/internal/programtest"
	"github.com/lycaon/lycaon/internal/prompts"
	"github.com/lycaon/lycaon/internal/sandbox"
	gittestsetup "github.com/lycaon/lycaon/internal/testsetup/git"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/toolcontract"
	"github.com/lycaon/lycaon/internal/toolexecution"
	"github.com/lycaon/lycaon/internal/tooloutput"
	"github.com/lycaon/lycaon/internal/toolprofiles"
	"github.com/lycaon/lycaon/internal/toolrejection"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/tools/native/command"
	"github.com/lycaon/lycaon/internal/tools/projectpaths"
)

type askComposedFixture struct {
	*programtest.Fixture
	privateGit *privategit.Manager
	launches   []lycexec.LaunchPlan
	launchMu   sync.Mutex
	askMode    bool
}

func newAskComposedFixture(t *testing.T) *askComposedFixture {
	t.Helper()
	gittestsetup.Enable()
	toolList := []string{
		"write", "read", "delete", "move", "copy", "mkdir", "chmod", "chown", "extract_archive",
		"grep", "find", "list_dir", "stat", "wc", "jq", "jq_edit", "http_request",
		"git_status", "git_diff", "git_log", "git_show", "git_blame", "git_branches",
		"git_ref", "git_compare", "git_stash_list", "git_commit", "git_checkout", "git_restore", "git_merge", "git_stash",
	}
	base := programtest.New(t, programtest.Options{Tools: toolList})
	allowedTools := map[string]bool{"command": true, "verify": true}
	for _, name := range toolList {
		allowedTools[name] = true
	}
	boundary := sandbox.NewBoundary(sandbox.Config{ProjectRootRequired: true, RejectSymlinkEscape: true}, []sandbox.ToolProfile{{
		ID: "fixture", Tools: allowedTools, CommandEffects: toolcontract.AllCommandEffects(),
	}})
	base.Boundary, base.Runtime.Boundary = boundary, boundary
	base.Runtime.Executor = toolexecution.NewExecutor(toolprofiles.NewProfilePolicyEngine(boundary), base.Runtime.Registry, "fixture")
	hints, err := guidance.LoadHintConfigStock()
	testutil.FailErr(t, "load hint catalog", err)
	guidance.SetGuidanceRenderer(prompts.NewGuidanceRenderer(prompts.NewFileTemplateEngineLayers(prompts.PromptLayers{})))
	base.Runtime.Executor.Rejections.SetRejectFormatter(guidance.NewStaticRejectFormatter(hints))

	af := &askComposedFixture{Fixture: base, askMode: true}
	base.Runtime.Owners = func(tc tools.ToolContext, paths *projectpaths.ProgramPaths, _ *toolcontract.ProgramMemory, _ tooloutput.SpillAdmission) (command.ProgramOwners, error) {
		return command.ProgramOwners{
			Natives: af.nativeOperations(),
			Publication: func(admit tools.CommandOutputAdmission) commandprogram.OutputPublication {
				return askRedirectPublication{paths: paths, tc: tc, admit: admit}
			},
			Redirections: func(frame *projectpaths.ProgramPaths, admit tools.CommandOutputAdmission) (commandprogram.InputRedirection, commandprogram.OutputPublication) {
				pub := askRedirectPublication{paths: frame, tc: tc, admit: admit}
				return pub, pub
			},
		}, nil
	}
	base.Configure = func(tc *tools.ToolContext) {
		tc.Source.AskMode, tc.Source.ReadOnly = af.askMode, af.askMode
	}
	return af
}

func (f *askComposedFixture) resolveOperandPath(paths *projectpaths.ProgramPaths, raw string) string {
	switch {
	case strings.HasPrefix(raw, "@output/"):
		return filepath.Join(f.OutputDir, strings.TrimPrefix(raw, "@output/"))
	case strings.HasPrefix(raw, "@scratch/"):
		return filepath.Join(f.ScratchDir, strings.TrimPrefix(raw, "@scratch/"))
	case filepath.IsAbs(raw):
		return raw
	default:
		return filepath.Join(paths.Cwd().Abs, filepath.FromSlash(raw))
	}
}

func (f *askComposedFixture) nativeOperations() map[string]command.NativeOperation {
	return map[string]command.NativeOperation{
		"cat": f.catOperation(), "sed": f.sedOperation(), "rg": f.rgOperation(), "find": f.findOperation(),
		"jq": f.jqOperation(), "tee": teeOperation(f.Dir), "mkdir": f.mutatingOperation("mkdir", "mkdir", "create"),
		"rm": f.mutatingOperation("rm", "delete", "remove"), "curl": f.curlOperation(), "git": f.gitOperation(),
	}
}

func (f *askComposedFixture) catOperation() command.NativeOperation {
	spec := catalogSpec("cat")
	return command.NativeOperation{Tool: "read", Prepare: func(_ context.Context, paths *projectpaths.ProgramPaths, _ commandprogram.Dispatch, req commandprogram.OperationRequest) (command.NativePrepared, error) {
		bound, err := commandprogram.CatalogBinder{}.Bind(spec, req.Argv)
		if err != nil {
			return command.NativePrepared{}, err
		}
		_, numberLines := bound.Last("-n")
		operands := bound.Operands().Values()
		return command.NativePrepared{Mode: "read", Review: bound.Review("read"), Invoke: func(_ context.Context, _ tools.ToolContext, streams commandprogram.Streams) (toolcontract.ProducerResult, error) {
			var raw bytes.Buffer
			if len(operands) == 0 {
				if _, err := io.Copy(&raw, streams.Input); err != nil {
					return toolcontract.ProducerResult{Status: 1, Terminal: toolcontract.ProducerFailed, EffectCertainty: toolcontract.NoEffect}, err
				}
			} else {
				for _, op := range operands {
					data, err := os.ReadFile(f.resolveOperandPath(paths, op))
					if err != nil {
						return toolcontract.ProducerResult{Status: 1, Terminal: toolcontract.ProducerFailed, EffectCertainty: toolcontract.NoEffect}, err
					}
					raw.Write(data)
				}
			}
			if !numberLines {
				_, _ = streams.Output.Write(raw.Bytes())
			} else {
				scanner := bufio.NewScanner(&raw)
				for lineNo := 1; scanner.Scan(); lineNo++ {
					_, _ = fmt.Fprintf(streams.Output, "%6d\t%s\n", lineNo, scanner.Text())
				}
			}
			return toolcontract.ProducerResult{Terminal: toolcontract.ProducerCompleted, Complete: true, PublicationEligible: true, EffectCertainty: toolcontract.NoEffect}, nil
		}}, nil
	}}
}

func (f *askComposedFixture) sedOperation() command.NativeOperation {
	spec := catalogSpec("sed")
	selectMode := func(_ context.Context, argv commandprogram.Fields) (string, bool) {
		for _, field := range argv[1:] {
			if field.Value == "-i" || strings.HasPrefix(field.Value, "-i") {
				return "-i", true
			}
		}
		return "stream-script", true
	}
	return command.NativeOperation{Tool: "sed", ClosedForms: true, SelectMode: selectMode, Prepare: func(ctx context.Context, paths *projectpaths.ProgramPaths, _ commandprogram.Dispatch, req commandprogram.OperationRequest) (command.NativePrepared, error) {
		bound, err := commandprogram.CatalogBinder{}.Bind(spec, req.Argv)
		if err != nil {
			return command.NativePrepared{}, err
		}
		mode, _ := selectMode(ctx, req.Argv)
		operands := bound.Operands().Values()
		return command.NativePrepared{Mode: mode, Review: bound.Review("sed"), Invoke: func(_ context.Context, _ tools.ToolContext, streams commandprogram.Streams) (toolcontract.ProducerResult, error) {
			if mode == "-i" {
				return toolcontract.ProducerResult{}, errors.New("sed -i must not run in Ask mode")
			}
			if len(operands) == 0 {
				return toolcontract.ProducerResult{Status: 1, Terminal: toolcontract.ProducerFailed, EffectCertainty: toolcontract.NoEffect}, errors.New("missing sed script")
			}
			var data []byte
			var readErr error
			if len(operands) > 1 {
				data, readErr = os.ReadFile(f.resolveOperandPath(paths, operands[1]))
			} else {
				data, readErr = io.ReadAll(streams.Input)
			}
			if readErr != nil {
				return toolcontract.ProducerResult{Status: 1, Terminal: toolcontract.ProducerFailed, EffectCertainty: toolcontract.NoEffect}, readErr
			}
			startLine, endLine := parseSedRange(operands[0])
			lines := strings.Split(strings.TrimSuffix(string(data), "\n"), "\n")
			for idx := startLine; idx <= endLine && idx >= 1 && idx <= len(lines); idx++ {
				_, _ = fmt.Fprintln(streams.Output, lines[idx-1])
			}
			return toolcontract.ProducerResult{Terminal: toolcontract.ProducerCompleted, Complete: true, PublicationEligible: true, EffectCertainty: toolcontract.NoEffect}, nil
		}}, nil
	}}
}

func parseSedRange(script string) (int, int) {
	parts := strings.SplitN(strings.TrimSuffix(strings.TrimSpace(script), "p"), ",", 2)
	start, _ := strconv.Atoi(parts[0])
	end := start
	if len(parts) == 2 {
		end, _ = strconv.Atoi(parts[1])
	}
	return start, end
}

func (f *askComposedFixture) rgOperation() command.NativeOperation {
	spec := catalogSpec("rg")
	return command.NativeOperation{Tool: "grep", Prepare: func(_ context.Context, paths *projectpaths.ProgramPaths, _ commandprogram.Dispatch, req commandprogram.OperationRequest) (command.NativePrepared, error) {
		bound, err := commandprogram.CatalogBinder{}.Bind(spec, req.Argv)
		if err != nil {
			return command.NativePrepared{}, err
		}
		operands := bound.Operands().Values()
		return command.NativePrepared{Mode: "search", Review: bound.Review("grep"), Invoke: func(_ context.Context, _ tools.ToolContext, streams commandprogram.Streams) (toolcontract.ProducerResult, error) {
			data, err := os.ReadFile(f.resolveOperandPath(paths, operands[1]))
			if err != nil {
				return toolcontract.ProducerResult{Status: 2, Terminal: toolcontract.ProducerFailed, EffectCertainty: toolcontract.NoEffect}, err
			}
			for i, line := range strings.Split(strings.TrimSuffix(string(data), "\n"), "\n") {
				if strings.Contains(line, operands[0]) {
					_, _ = fmt.Fprintf(streams.Output, "%d:%s\n", i+1, line)
				}
			}
			return toolcontract.ProducerResult{Terminal: toolcontract.ProducerCompleted, Complete: true, PublicationEligible: true, EffectCertainty: toolcontract.NoEffect}, nil
		}}, nil
	}}
}

func (f *askComposedFixture) findOperation() command.NativeOperation {
	spec := catalogSpec("find")
	return command.NativeOperation{Tool: "find", Prepare: func(_ context.Context, paths *projectpaths.ProgramPaths, _ commandprogram.Dispatch, req commandprogram.OperationRequest) (command.NativePrepared, error) {
		bound, err := commandprogram.CatalogBinder{}.Bind(spec, req.Argv)
		if err != nil {
			return command.NativePrepared{}, err
		}
		return command.NativePrepared{Mode: "traverse", Review: bound.Review("find"), Invoke: func(_ context.Context, _ tools.ToolContext, streams commandprogram.Streams) (toolcontract.ProducerResult, error) {
			operands := bound.Operands().Values()
			rootRel, pattern := ".", "*"
			if len(operands) > 0 {
				rootRel = operands[0]
			}
			if opt, ok := bound.Last("-name"); ok && len(opt.Values) > 0 {
				pattern = opt.Values[0].Value
			}
			_ = filepath.WalkDir(f.resolveOperandPath(paths, rootRel), func(p string, d fs.DirEntry, walkErr error) error {
				if walkErr == nil && !d.IsDir() {
					if matched, _ := filepath.Match(pattern, d.Name()); matched {
						rel, _ := filepath.Rel(paths.Cwd().Abs, p)
						_, _ = fmt.Fprintln(streams.Output, filepath.ToSlash(rel))
					}
				}
				return walkErr
			})
			return toolcontract.ProducerResult{Terminal: toolcontract.ProducerCompleted, Complete: true, PublicationEligible: true, EffectCertainty: toolcontract.NoEffect}, nil
		}}, nil
	}}
}

func (f *askComposedFixture) jqOperation() command.NativeOperation {
	spec := catalogSpec("jq")
	return command.NativeOperation{Tool: "jq", Prepare: func(_ context.Context, paths *projectpaths.ProgramPaths, _ commandprogram.Dispatch, req commandprogram.OperationRequest) (command.NativePrepared, error) {
		bound, err := commandprogram.CatalogBinder{}.Bind(spec, req.Argv)
		if err != nil {
			return command.NativePrepared{}, err
		}
		operands := bound.Operands().Values()
		return command.NativePrepared{Mode: "query", Review: bound.Review("jq"), Invoke: func(_ context.Context, _ tools.ToolContext, streams commandprogram.Streams) (toolcontract.ProducerResult, error) {
			data, err := os.ReadFile(f.resolveOperandPath(paths, operands[1]))
			if err != nil {
				return toolcontract.ProducerResult{Status: 2, Terminal: toolcontract.ProducerFailed, EffectCertainty: toolcontract.NoEffect}, err
			}
			var parsed struct {
				Items []struct {
					Name string `json:"name"`
				} `json:"items"`
			}
			if err := json.Unmarshal(data, &parsed); err != nil || len(parsed.Items) == 0 {
				return toolcontract.ProducerResult{Status: 1, Terminal: toolcontract.ProducerFailed, EffectCertainty: toolcontract.NoEffect}, err
			}
			_, _ = fmt.Fprintln(streams.Output, parsed.Items[0].Name)
			return toolcontract.ProducerResult{Terminal: toolcontract.ProducerCompleted, Complete: true, PublicationEligible: true, EffectCertainty: toolcontract.NoEffect}, nil
		}}, nil
	}}
}

func (f *askComposedFixture) mutatingOperation(cmdName, toolName, mode string) command.NativeOperation {
	spec := catalogSpec(cmdName)
	return command.NativeOperation{Tool: toolName, Prepare: func(_ context.Context, _ *projectpaths.ProgramPaths, _ commandprogram.Dispatch, req commandprogram.OperationRequest) (command.NativePrepared, error) {
		bound, err := commandprogram.CatalogBinder{}.Bind(spec, req.Argv)
		if err != nil {
			return command.NativePrepared{}, err
		}
		return command.NativePrepared{Mode: mode, Review: bound.Review(toolName), Invoke: func(context.Context, tools.ToolContext, commandprogram.Streams) (toolcontract.ProducerResult, error) {
			return toolcontract.ProducerResult{}, fmt.Errorf("%s must not execute in Ask mode", cmdName)
		}}, nil
	}}
}

func (f *askComposedFixture) curlOperation() command.NativeOperation {
	spec := catalogSpec("curl")
	return command.NativeOperation{Tool: "http_request", SecretSlots: []string{"operands", "options"}, Prepare: func(_ context.Context, _ *projectpaths.ProgramPaths, _ commandprogram.Dispatch, req commandprogram.OperationRequest) (command.NativePrepared, error) {
		bound, err := commandprogram.CatalogBinder{}.Bind(spec, req.Argv)
		if err != nil {
			return command.NativePrepared{}, err
		}
		return command.NativePrepared{Mode: "request", Review: bound.Review("http_request"), Invoke: func(context.Context, tools.ToolContext, commandprogram.Streams) (toolcontract.ProducerResult, error) {
			return toolcontract.ProducerResult{}, errors.New("curl must not execute in Ask mode")
		}}, nil
	}}
}

func (f *askComposedFixture) gitOperation() command.NativeOperation {
	gitTools := []string{"git_status", "git_diff", "git_log", "git_show", "git_blame", "git_branches", "git_ref", "git_compare", "git_stash_list", "git_commit", "git_checkout", "git_restore", "git_merge", "git_stash"}
	toolByForm := map[string]string{"git status": "git_status", "git diff": "git_diff", "git log": "git_log", "git show": "git_show", "git blame": "git_blame", "git branch": "git_branches", "git commit": "git_commit", "git checkout": "git_checkout", "git restore": "git_restore"}
	return command.NativeOperation{Tools: gitTools, Prepare: func(_ context.Context, paths *projectpaths.ProgramPaths, _ commandprogram.Dispatch, req commandprogram.OperationRequest) (command.NativePrepared, error) {
		spec, ok, err := commandprogram.ResolveCommand(req.Argv)
		if err != nil || !ok || !strings.HasPrefix(spec.Name, "git ") {
			return command.NativePrepared{}, &toolrejection.ToolReject{Code: "COMMAND_NATIVE_FORM_UNSUPPORTED", Data: map[string]any{"command": "git"}}
		}
		bound, err := commandprogram.CatalogBinder{}.Bind(spec, req.Argv)
		if err != nil {
			return command.NativePrepared{}, err
		}
		mode := ""
		if len(spec.Effects) > 0 {
			mode = spec.Effects[0].Mode
		}
		tool := toolByForm[spec.Name]
		reviewID := tool
		if reviewID == "" {
			reviewID = spec.Name
		}
		subArgs, cmdLine := req.Argv.Values()[1:], strings.Join(req.Argv.Values(), " ")
		return command.NativePrepared{Command: spec.Name, Tool: tool, Mode: mode, Review: bound.Review(reviewID), LaunchesConfined: true, Invoke: func(ctx context.Context, tc tools.ToolContext, streams commandprogram.Streams) (toolcontract.ProducerResult, error) {
			if tc.Identity.WorkerJobID != "" && strings.TrimSpace(tc.Source.WorkerBranchRoot) != "" && f.privateGit != nil {
				_, finish, _, viewErr := privategit.PrepareManagerViewOperation(ctx, f.privateGit, tools.HostWriteRoot(tc), tc.Identity.WorkerJobID)
				if viewErr != nil && !errors.Is(viewErr, fs.ErrNotExist) && !errors.Is(viewErr, privategit.ErrNoSourceRepository) {
					return toolcontract.ProducerResult{Status: 2, Terminal: toolcontract.ProducerFailed, EffectCertainty: toolcontract.NoEffect}, viewErr
				}
				if finish != nil {
					defer func() { _ = finish() }()
				}
			}
			plan, release, launchErr := command.BindAgentLaunch(ctx, tc, "git", cmdLine, nil)
			if launchErr != nil {
				return toolcontract.ProducerResult{Status: 2, Terminal: toolcontract.ProducerFailed, EffectCertainty: toolcontract.NoEffect}, launchErr
			}
			f.launchMu.Lock()
			f.launches = append(f.launches, plan)
			f.launchMu.Unlock()
			defer release(context.WithoutCancel(ctx), tools.AgentLaunchOutcome{})
			ctx = gitexec.WithAgentLaunch(ctx, gitexec.AgentLaunch{Plan: plan})
			diagnostics, status, readErr := (git.CommandReader{}).ReadCommand(ctx, paths.Cwd().Abs, subArgs, nil, streams.Output)
			if readErr != nil {
				return toolcontract.ProducerResult{Status: 2, Terminal: toolcontract.ProducerFailed, EffectCertainty: toolcontract.NoEffect}, readErr
			}
			if len(diagnostics) > 0 && streams.Error != nil {
				_, _ = streams.Error.Write(diagnostics)
			}
			return toolcontract.ProducerResult{Status: status, Terminal: toolcontract.ProducerCompleted, Complete: true, PublicationEligible: status == 0, EffectCertainty: toolcontract.NoEffect}, nil
		}}, nil
	}}
}

type askRedirectPublication struct {
	paths *projectpaths.ProgramPaths
	tc    tools.ToolContext
	admit tools.CommandOutputAdmission
}

func (p askRedirectPublication) OpenInput(_ context.Context, dest commandprogram.InputDestination) (io.ReadCloser, error) {
	return os.Open(filepath.Join(p.paths.Cwd().Abs, filepath.FromSlash(dest.Target.Value)))
}

func (p askRedirectPublication) Open(ctx context.Context, dest commandprogram.OutputDestination) (commandprogram.StagedOutput, error) {
	targetAbs := filepath.Join(p.paths.Cwd().Abs, filepath.FromSlash(dest.Target.Value))
	write, err := p.admit(ctx, dest, targetAbs, dest.Target.Value)
	if err != nil {
		return nil, err
	}
	return &askStagedRedirect{targetAbs: targetAbs, write: write}, nil
}

type askStagedRedirect struct {
	buf       bytes.Buffer
	targetAbs string
	write     tools.CommandOutputWrite
}

func (s *askStagedRedirect) Write(p []byte) (int, error) { return s.buf.Write(p) }

func (s *askStagedRedirect) Publish(ctx context.Context) (commandprogram.OutputPublished, error) {
	return s.write.Publish(ctx, func(_ context.Context, _ tools.ToolContext) (commandprogram.OutputPublished, error) {
		if err := os.WriteFile(s.targetAbs, s.buf.Bytes(), 0o644); err != nil {
			return commandprogram.OutputPublished{}, err
		}
		return commandprogram.OutputPublished{PublishedRevision: "sha256:published"}, nil
	})
}

func (s *askStagedRedirect) Abandon(ctx context.Context) error { return s.write.Abandon(ctx, nil) }

func (f *askComposedFixture) resetLaunches() {
	f.launchMu.Lock()
	f.launches = nil
	f.launchMu.Unlock()
}

func (f *askComposedFixture) recordedLaunches() []lycexec.LaunchPlan {
	f.launchMu.Lock()
	defer f.launchMu.Unlock()
	return slices.Clone(f.launches)
}
