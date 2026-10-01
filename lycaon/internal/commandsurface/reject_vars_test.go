package commandsurface_test

import (
	"fmt"
	"testing"

	"github.com/lycaon/lycaon/internal/argv"
	"github.com/lycaon/lycaon/internal/commandsurface"
)

// These fields mirror each tool schema.
var (
	commandFields = []string{"command", "pipeline", "stdin", "stdout_to", "stderr_to", "background", "cwd", "env"}
	ptyFields     = []string{"command", "cwd", "env", "winsize", "observe"}
)

func TestCommandRejectVarsShellMetachar(t *testing.T) {
	vars := commandsurface.CommandRejectVars("implement", "go test; rm", "go test; rm", commandFields, fmt.Errorf("command contains shell metacharacters"))
	if vars["shell_metacharacters"] != false {
		t.Fatalf("untyped prose must not classify: %#v", vars)
	}
}

func TestCommandRejectVarsExecMetachar(t *testing.T) {
	vars := commandsurface.CommandRejectVars("implement", "go test; rm", "go test; rm", commandFields, argv.ErrShellMetacharacters)
	if vars["shell_metacharacters"] != true {
		t.Fatalf("expected shell_metacharacters=true: %#v", vars)
	}
}

func TestCommandRejectVarsQuotedPipeIsNotComposition(t *testing.T) {
	vars := commandsurface.CommandRejectVars("implement", `echo $X "a|b"`, `echo $X "a|b"`, commandFields, argv.ErrShellMetacharacters)
	if vars["use_pipeline_array"] != false {
		t.Fatalf("quoted | must not select pipeline advice: %#v", vars)
	}
	if vars["shell_metacharacters"] != true {
		t.Fatalf("expected shell_metacharacters=true: %#v", vars)
	}
}

func TestCommandRejectVarsPipelineFormNeverBlamesSynthesizedPipe(t *testing.T) {
	// Pipeline fixes must target the raw command field.
	vars := commandsurface.CommandRejectVars("implement", "grep a | head", "", commandFields, fmt.Errorf("pipeline stage 0: %w", argv.ErrCommandRequired))
	if vars["use_pipeline_array"] != false {
		t.Fatalf("pipeline-form reject must not select pipeline advice: %#v", vars)
	}
}

// PTY fixes must use fields accepted by its schema.
func TestCommandRejectVarsPtyOffersNoCompositionParameter(t *testing.T) {
	vars := commandsurface.CommandRejectVars("implement", "npm run dev | tee log", "npm run dev | tee log", ptyFields, argv.ErrShellMetacharacters)
	if vars["use_pipeline_array"] != false {
		t.Fatalf("a surface without `pipeline` must not advise it: %#v", vars)
	}
	if vars["reissue_via_command"] != true {
		t.Fatalf("expected the reissue-elsewhere branch: %#v", vars)
	}
}

func TestCommandRejectVarsSequenceOnOneProcessSurface(t *testing.T) {
	vars := commandsurface.CommandRejectVars("implement", "go build && ./srv", "go build && ./srv", ptyFields, commandsurface.ErrSequenceUnsupported)
	if vars["sequence_unsupported"] != true {
		t.Fatalf("expected sequence_unsupported: %#v", vars)
	}
	if vars["reissue_via_command"] != true {
		t.Fatalf("expected the reissue-elsewhere branch: %#v", vars)
	}
}

func TestCommandRejectVarsEnvironmentAssignment(t *testing.T) {
	vars := commandsurface.CommandRejectVars(
		"implement", "TOKEN=value", "TOKEN=value", commandFields,
		argv.ErrEnvAssignmentCommand,
	)
	if vars["env_assignment"] != true {
		t.Fatalf("expected env_assignment=true: %#v", vars)
	}
}

// Command fixes retain every supported branch.
func TestCommandRejectVarsCommandKeepsPipelineAdvice(t *testing.T) {
	vars := commandsurface.CommandRejectVars("implement", "git log | head", "git log | head", commandFields, argv.ErrShellMetacharacters)
	if vars["use_pipeline_array"] != true {
		t.Fatalf("command carries `pipeline`; advice must stand: %#v", vars)
	}
	if vars["reissue_via_command"] != false {
		t.Fatalf("command is the carrier; it must not be told to reissue: %#v", vars)
	}
}

func TestCommandRejectVarsScratchDir(t *testing.T) {
	_, _, _, err := argv.SplitCommandLine("cat $SCRATCH_DIR/foo")
	if err == nil {
		t.Fatal("expected SplitCommandLine to reject unquoted $SCRATCH_DIR")
	}
	vars := commandsurface.CommandRejectVars("implement", "cat $SCRATCH_DIR/foo", "cat $SCRATCH_DIR/foo", commandFields, err)
	if vars["scratch_var"] != true {
		t.Fatalf("expected scratch_var=true: %#v", vars)
	}
	if vars["use_substitution_result"] != true {
		t.Fatalf("expected use_substitution_result=true: %#v", vars)
	}

	_, _, _, errOther := argv.SplitCommandLine("cat $FOO/bar")
	varsOther := commandsurface.CommandRejectVars("implement", "cat $FOO/bar", "cat $FOO/bar", commandFields, errOther)
	if varsOther["scratch_var"] != false {
		t.Fatalf("expected scratch_var=false for $FOO: %#v", varsOther)
	}
}
