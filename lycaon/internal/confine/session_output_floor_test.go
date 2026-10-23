package confine_test

import (
	"bufio"
	"bytes"
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/confine"
	"github.com/lycaon/lycaon/internal/scratch"
	"github.com/lycaon/lycaon/internal/testutil"
)

// New copies are published after the process enters its profile. The alias
// case instead keeps a previously published inode through a later launch.
func sessionOutputKernelProof(t *testing.T, helper string) {
	for _, mode := range []string{"cat", "sed", "chmod", "unlink", "overwrite", "replace", "rename-output-parent", "symlink-overwrite", "hardlink", "reviewed-parent-rename", "process-control-chmod", "temporary-state-chmod", "temporary-state-ancestor-rename", "host-execution-chmod", "preexisting-hardlink-overwrite"} {
		t.Run(mode, func(t *testing.T) {
			base := outsideTemporaryWriteRoots(t)
			if mode == "temporary-state-chmod" || mode == "temporary-state-ancestor-rename" {
				base = t.TempDir()
			}
			host := filepath.Join(base, "host")
			cfg := filepath.Join(host, "state")
			testutil.FailErr(t, "create host state", os.MkdirAll(cfg, 0700))
			t.Setenv("LYCAON_CONFIG_DIR", cfg)
			t.Setenv("LYCAON_SANDBOX_DENY_READ", "")
			dirs, err := scratch.New(cfg).Ensure("chat")
			testutil.FailErr(t, "prepare session folders", err)
			project := filepath.Join(base, "project")
			testutil.FailErr(t, "create writable project", os.Mkdir(project, 0700))
			target := filepath.Join(dirs.Output, "program.stdout")
			staged := filepath.Join(project, "replacement")
			testutil.FailErr(t, "replacement bytes", os.WriteFile(staged, []byte("changed\n"), 0600))
			alias := filepath.Join(project, "alias")
			testutil.FailErr(t, "source alias", os.Symlink(target, alias))
			if mode == "preexisting-hardlink-overwrite" {
				testutil.FailErr(t, "publish before later alias launch", scratch.PublishCommandOutput(dirs.Output, "program", "stdout", strings.NewReader("one\ntwo\n")))
				testutil.FailErr(t, "plant prior outside inode alias", os.Link(target, filepath.Join(project, "prior-link")))
			}
			c := confine.Confinement{Roots: []string{project}, SessionScratchRoot: dirs.Scratch, ReadRoots: []string{dirs.Output}, Network: confine.NetworkDeny}
			if mode == "host-execution-chmod" {
				c.HostExecution = true
			}
			if mode == "process-control-chmod" {
				c.ProcessControl = true
			}
			moved := host + "-moved"
			t.Cleanup(func() { testutil.FailErr(t, "remove owned moved fixture", os.RemoveAll(moved)) })
			if mode == "reviewed-parent-rename" {
				c.GrantedWriteRoots = []string{base}
			}
			mutation := `/bin/cat "$1"`
			switch mode {
			case "sed":
				mutation = `/usr/bin/sed -n '2p' "$1"`
			case "chmod", "process-control-chmod", "temporary-state-chmod", "host-execution-chmod":
				mutation = `/bin/chmod u+w "$1"`
			case "unlink":
				mutation = `/bin/rm "$1"`
			case "overwrite":
				mutation = `printf changed > "$1"`
			case "replace":
				mutation = `/bin/mv "$2/replacement" "$1"`
			case "rename-output-parent":
				mutation = `/bin/mv "$3" "$3-moved"`
			case "symlink-overwrite":
				mutation = `printf changed > "$2/alias"`
			case "preexisting-hardlink-overwrite":
				mutation = `/bin/chmod u+w "$2/prior-link" && printf changed > "$2/prior-link"`
			case "hardlink":
				mutation = `/bin/ln "$1" "$2/hardlink"`
			case "reviewed-parent-rename", "temporary-state-ancestor-rename":
				mutation = `/bin/mv "$4" "$5"`
			}
			// Keep the protocol and success controls independent of diagnostic wording.
			script := `printf 'ready\n'; read -r token || exit 90; /bin/cat "$1" || exit 91; printf sibling > "$2/sibling" || exit 92; ` + mutation
			ctx, cancel := context.WithTimeout(t.Context(), testutil.Timeout(10*time.Second))
			defer cancel()
			cmd, cleanup, err := confine.Command(ctx, helper, "/bin/sh", []string{"-c", script, "sh", target, project, filepath.Dir(dirs.Output), host, moved}, c)
			testutil.FailErr(t, "prepare ordinary profile", err)
			defer cleanup()
			input, err := cmd.StdinPipe()
			testutil.FailErr(t, "child gate", err)
			output, err := cmd.StdoutPipe()
			testutil.FailErr(t, "child output", err)
			var diagnostic bytes.Buffer
			cmd.Stderr = &diagnostic
			testutil.FailErr(t, "start confined child", cmd.Start())
			reader := bufio.NewReader(output)
			ready, err := reader.ReadString('\n')
			testutil.FailErr(t, "entered profile: "+diagnostic.String(), err)
			if ready != "ready\n" {
				t.Fatalf("child protocol=%q", ready)
			}
			if mode != "preexisting-hardlink-overwrite" {
				if _, err := os.Stat(target); !os.IsNotExist(err) {
					t.Fatalf("copy existed before host publication: %v", err)
				}
				testutil.FailErr(t, "publish after process start", scratch.PublishCommandOutput(dirs.Output, "program", "stdout", strings.NewReader("one\ntwo\n")))
			}
			if mode == "hardlink" {
				control := filepath.Join(project, "host-link-control")
				testutil.FailErr(t, "host link control on same filesystem", os.Link(target, control))
				testutil.FailErr(t, "remove control link before attempt", os.Remove(control))
			}
			_, err = io.WriteString(input, "continue\n")
			testutil.FailErr(t, "release child", err)
			input.Close()
			delivered, readErr := io.ReadAll(reader)
			waitErr := cmd.Wait()
			testutil.FailErr(t, "read process output", readErr)
			if !strings.HasPrefix(string(delivered), "one\ntwo\n") {
				t.Fatalf("output read failed: %q %v %s", delivered, waitErr, diagnostic.String())
			}
			sibling, err := os.ReadFile(filepath.Join(project, "sibling"))
			testutil.FailErr(t, "ordinary sibling write", err)
			if string(sibling) != "sibling" {
				t.Fatalf("sibling=%q", sibling)
			}
			if mode == "cat" || mode == "sed" {
				testutil.FailErr(t, "read command: "+diagnostic.String(), waitErr)
			} else if waitErr == nil {
				t.Fatalf("external %s changed host-owned output", mode)
			}
			retained, err := os.ReadFile(target)
			testutil.FailErr(t, "copy remains at its address", err)
			if string(retained) != "one\ntwo\n" {
				t.Fatalf("copy changed=%q", retained)
			}
		})
	}
}
