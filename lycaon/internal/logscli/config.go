package logscli

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/lycaon/lycaon/internal/configdir"
	execpkg "github.com/lycaon/lycaon/internal/exec"
	"github.com/lycaon/lycaon/internal/logview"
)

func configUsage(prog string) string {
	return fmt.Sprintf(`usage: %s config [get KEY | set KEY VALUE | path | edit]

With no arguments, prints all settings. Config lives at %s/logview.yaml.`, prog, configdir.Label())
}

func runConfig(prog string, f flags, cfg logview.Config) error {
	args := f.pos
	if len(args) == 0 {
		for _, k := range logview.ConfigKeys() {
			v, err := cfg.GetField(k)
			if err != nil {
				return err
			}
			_, _ = fmt.Fprintf(os.Stdout, "%-14s %s\n", k, v)
		}
		return nil
	}

	switch args[0] {
	case "path":
		p, err := logview.ConfigPath()
		if err != nil {
			return err
		}
		_, _ = fmt.Fprintln(os.Stdout, p)
		return nil
	case "get":
		if len(args) < 2 {
			return fmt.Errorf("usage: %s config get KEY", prog)
		}
		v, err := cfg.GetField(args[1])
		if err != nil {
			return err
		}
		_, _ = fmt.Fprintln(os.Stdout, v)
		return nil
	case "set":
		if len(args) < 3 {
			return fmt.Errorf("usage: %s config set KEY VALUE", prog)
		}
		if err := cfg.SetField(args[1], args[2]); err != nil {
			return err
		}
		if err := cfg.Save(); err != nil {
			return err
		}
		_, _ = fmt.Fprintf(os.Stdout, "set %s = %s\n", args[1], args[2])
		return nil
	case "edit":
		return editConfig(cfg)
	default:
		return fmt.Errorf("unknown config command %q\n\n%s", args[0], configUsage(prog))
	}
}

func editConfig(cfg logview.Config) error {
	if err := cfg.Save(); err != nil {
		return err
	}
	path, err := logview.ConfigPath()
	if err != nil {
		return err
	}
	editor := strings.TrimSpace(os.Getenv("EDITOR"))
	if editor == "" {
		editor = "vi"
	}
	cmd, cleanup, err := execpkg.PrepareCommand(context.Background(), editor, []string{path}, execpkg.ExecOpts{
		Launch: execpkg.HostLaunch("log_config_editor"),
	})
	if err != nil {
		return err
	}
	defer cleanup()
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	return cmd.Run()
}
