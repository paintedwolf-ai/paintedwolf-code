package commandsurface_test

import (
	"errors"
	"fmt"
	"testing"

	"github.com/lycaon/lycaon/internal/argv"
	"github.com/lycaon/lycaon/internal/commandsurface"
)

func TestCommandRejectVarsShellMetachar(t *testing.T) {
	vars := commandsurface.CommandRejectVars("implement", "go test; rm", "go test; rm", fmt.Errorf("command contains shell metacharacters"))
	if vars["shell_metacharacters"] != false {
		t.Fatalf("untyped prose must not classify: %#v", vars)
	}
}

func TestCommandRejectVarsExecMetachar(t *testing.T) {
	vars := commandsurface.CommandRejectVars("implement", "go test; rm", "go test; rm", argv.ErrShellMetacharacters)
	if vars["shell_metacharacters"] != true {
		t.Fatalf("expected shell_metacharacters=true: %#v", vars)
	}
}

func TestCommandRejectVarsQuotedPipeIsNotReissue(t *testing.T) {
	vars := commandsurface.CommandRejectVars("implement", `echo $X "a|b"`, `echo $X "a|b"`, argv.ErrShellMetacharacters)
	if vars["reissue_via_command"] != false {
		t.Fatalf("quoted | must not select reissue advice: %#v", vars)
	}
	if vars["shell_metacharacters"] != true {
		t.Fatalf("expected shell_metacharacters=true: %#v", vars)
	}
}

func TestCommandRejectVarsUnquotedPipeReissuesViaCommand(t *testing.T) {
	vars := commandsurface.CommandRejectVars("implement", "npm run dev | tee log", "npm run dev | tee log", argv.ErrShellMetacharacters)
	if vars["reissue_via_command"] != true {
		t.Fatalf("expected the reissue-elsewhere branch: %#v", vars)
	}
}

func TestCommandRejectVarsPipelineFormNeverBlamesSynthesizedPipe(t *testing.T) {
	// Pipeline fixes must target the raw command field.
	vars := commandsurface.CommandRejectVars("implement", "grep a | head", "", fmt.Errorf("pipeline stage 0: %w", argv.ErrCommandRequired))
	if vars["reissue_via_command"] != false {
		t.Fatalf("pipeline-form reject must not select reissue advice: %#v", vars)
	}
}

func TestCommandRejectVarsSequenceOnOneProcessSurface(t *testing.T) {
	vars := commandsurface.CommandRejectVars("implement", "go build && ./srv", "go build && ./srv", commandsurface.ErrSequenceUnsupported)
	if vars["sequence_unsupported"] != true {
		t.Fatalf("expected sequence_unsupported: %#v", vars)
	}
	if vars["reissue_via_command"] != true {
		t.Fatalf("expected the reissue-elsewhere branch: %#v", vars)
	}
}

func TestCommandRejectVarsEnvironmentAssignment(t *testing.T) {
	vars := commandsurface.CommandRejectVars(
		"implement", "TOKEN=value", "TOKEN=value",
		argv.ErrEnvAssignmentCommand,
	)
	if vars["env_assignment"] != true {
		t.Fatalf("expected env_assignment=true: %#v", vars)
	}
}

func TestCommandRejectVarsDirectoryChange(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
		want bool
	}{
		{"typed", commandsurface.ErrDirectoryChangeCommand, true},
		{"wrapped", fmt.Errorf("stage 0: %w", commandsurface.ErrDirectoryChangeCommand), true},
		{"prose", errors.New(commandsurface.ErrDirectoryChangeCommand.Error()), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			vars := commandsurface.CommandRejectVars("implement", "cd src && tool --check", "cd src && tool --check", tc.err)
			if vars["directory_change"] != tc.want {
				t.Fatalf("directory_change = %v, want %v", vars["directory_change"], tc.want)
			}
		})
	}
}

func TestCommandRejectVarsHeredocReissuesViaCommand(t *testing.T) {
	_, _, _, err := argv.SplitCommandLine("cat <<'EOF' > out.txt")
	if err == nil {
		t.Fatal("expected SplitCommandLine to reject a here-document")
	}
	vars := commandsurface.CommandRejectVars("implement", "cat <<'EOF' > out.txt", "cat <<'EOF' > out.txt", err)
	if vars["shell_metacharacters"] != true {
		t.Fatalf("expected shell_metacharacters=true: %#v", vars)
	}
	if vars["reissue_via_command"] != true {
		t.Fatalf("expected the reissue-elsewhere branch: %#v", vars)
	}
}

func TestCommandRejectVarsBackgroundOperatorReissues(t *testing.T) {
	_, _, _, err := argv.SplitCommandLine("python3 -m http.server 8000 &")
	if err == nil {
		t.Fatal("expected SplitCommandLine to reject a bare background operator")
	}
	vars := commandsurface.CommandRejectVars("implement", "python3 -m http.server 8000 &", "python3 -m http.server 8000 &", err)
	if vars["reissue_via_command"] != true {
		t.Fatalf("expected the reissue-elsewhere branch: %#v", vars)
	}
}

func TestCommandRejectVarsScratchDir(t *testing.T) {
	_, _, _, err := argv.SplitCommandLine("cat $SCRATCH_DIR/foo")
	if err == nil {
		t.Fatal("expected SplitCommandLine to reject unquoted $SCRATCH_DIR")
	}
	vars := commandsurface.CommandRejectVars("implement", "cat $SCRATCH_DIR/foo", "cat $SCRATCH_DIR/foo", err)
	if vars["scratch_var"] != true {
		t.Fatalf("expected scratch_var=true: %#v", vars)
	}

	_, _, _, errOther := argv.SplitCommandLine("cat $FOO/bar")
	varsOther := commandsurface.CommandRejectVars("implement", "cat $FOO/bar", "cat $FOO/bar", errOther)
	if varsOther["scratch_var"] != false {
		t.Fatalf("expected scratch_var=false for $FOO: %#v", varsOther)
	}
}

