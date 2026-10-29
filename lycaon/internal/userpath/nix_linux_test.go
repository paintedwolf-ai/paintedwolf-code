//go:build linux

package userpath

import (
	"context"
	"errors"
	"io/fs"
	"os"
	osexec "os/exec"
	"strings"
	"testing"
)

func TestLinuxAccountLookupUsesSystemConventionsWithoutPersonPath(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	for _, present := range []string{"/usr/bin/getent", "/run/current-system/sw/bin/getent", ""} {
		t.Run(present, func(t *testing.T) {
			var seen []string
			program, err := linuxAccountLookupPath(func(path string) (fs.FileInfo, error) {
				seen = append(seen, path)
				if path != present {
					return nil, os.ErrNotExist
				}
				return executableStub{name: "getent"}, nil
			})
			if present == "" {
				if err == nil || program != "" || len(seen) != 2 {
					t.Fatalf("missing system lookup = %q, %v, paths=%v", program, err, seen)
				}
			} else if err != nil || program != present {
				t.Fatalf("system lookup = %q, %v; want %q", program, err, present)
			}
		})
	}
	denied := errors.New("host identity lookup denied")
	if _, err := linuxAccountLookupPath(func(string) (fs.FileInfo, error) { return nil, denied }); !errors.Is(err, denied) {
		t.Fatalf("lookup substituted a different utility after host refusal: %v", err)
	}
	directory, err := os.Stat(t.TempDir())
	if err != nil {
		t.Fatalf("inspect non-executable fixture: %v", err)
	}
	if _, err := linuxAccountLookupPath(func(string) (fs.FileInfo, error) { return directory, nil }); err == nil {
		t.Fatal("lookup accepted a directory as the NSS utility")
	}
}

func TestNixStoreAccountShellPreservesProfilePathWithoutProjectHooks(t *testing.T) {
	cfg := testConfig()
	shell := "/nix/store/00000000000000000000000000000000-bash-5.3/bin/bash"
	path := "/home/person/.nix-profile/bin:/etc/profiles/per-user/person/bin:/nix/var/nix/profiles/default/bin:/run/current-system/sw/bin:/usr/bin"
	provider := providerWith(t, cfg, shell, map[string]string{"HOME": "/home/person", "PATH": "/usr/bin"}, fakeShell{stdout: framed(cfg.Probe.Marker, path)})
	provider.accountShell = func(context.Context) (string, error) { return shell, nil }
	inner := provider.run
	provider.run = func(command *osexec.Cmd) error {
		if command.Path != shell || command.Dir != "" || strings.Contains(strings.Join(command.Args, " "), "nix develop") || strings.Contains(strings.Join(command.Args, " "), "direnv") {
			t.Fatalf("account discovery launched a project environment: path=%s dir=%s args=%v", command.Path, command.Dir, command.Args)
		}
		return inner(command)
	}
	snapshot := provider.Resolve(t.Context())
	if snapshot.Source() != SourceProbe || snapshot.Value() != path {
		t.Fatalf("Nix account PATH = %q (%s, %s)", snapshot.Value(), snapshot.Source(), snapshot.Reason())
	}
}
