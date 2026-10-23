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

// The folder exists before the process starts; its copy is published only after
// the child announces it has entered the already-compiled ordinary profile.
func sessionOutputKernelProof(t *testing.T, helper string) {
	for _, mode := range []string{"cat", "sed", "chmod", "unlink", "overwrite", "replace", "rename-output-parent", "symlink-overwrite", "hardlink", "reviewed-parent-rename", "process-control-chmod", "temporary-state-chmod"} {
		t.Run(mode, func(t *testing.T) {
			base := outsideTemporaryWriteRoots(t)
			if mode == "temporary-state-chmod" {
				base = t.TempDir()
			}
			host := filepath.Join(base, "host")
			cfg := filepath.Join(host, "state")
			testutil.FailErr(t, "create host state", os.MkdirAll(cfg, 0700))
			t.Setenv("LYCAON_CONFIG_DIR", cfg)
			t.Setenv("LYCAON_SANDBOX_DENY_READ", "")
			dirs, err := scratch.New(cfg).Ensure("chat")
			testutil.FailErr(t, "prepare session folders", err)
			project := t.TempDir()
			target := filepath.Join(dirs.Output, "program.stdout")
			staged := filepath.Join(project, "replacement")
			testutil.FailErr(t, "replacement bytes", os.WriteFile(staged, []byte("changed\n"), 0600))
			alias := filepath.Join(project, "alias")
			testutil.FailErr(t, "source alias", os.Symlink(target, alias))
			c := confine.Confinement{Roots: []string{project}, SessionScratchRoot: dirs.Scratch, ReadRoots: []string{dirs.Output}, Network: confine.NetworkDeny}
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
			case "chmod", "process-control-chmod", "temporary-state-chmod":
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
			case "hardlink":
				mutation = `/bin/ln "$1" "$2/hardlink"`
			case "reviewed-parent-rename":
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
			if _, err := os.Stat(target); !os.IsNotExist(err) {
				t.Fatalf("copy existed before host publication: %v", err)
			}
			testutil.FailErr(t, "publish after process start", scratch.PublishCommandOutput(dirs.Output, "program", "stdout", strings.NewReader("one\ntwo\n")))
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
