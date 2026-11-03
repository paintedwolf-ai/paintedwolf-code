package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
)

// Copies of the test executable exercise the real command driver without Nix/network access.
func TestMain(m *testing.M) {
	if os.Getenv("PW_BUN_COMMAND_FIXTURE") == "1" {
		os.Exit(runBunFixtureCommand())
	}
	os.Exit(m.Run())
}

func runBunFixtureCommand() int {
	switch strings.TrimSuffix(filepath.Base(os.Args[0]), ".exe") {
	case "nix":
		if os.Getenv("PW_BUN_COMMAND_FAILURE") == "1" {
			return 23
		}
		if len(os.Args) != 7 || os.Args[6] != "github:nix-community/bun2nix/"+strings.Repeat("a", 40) {
			return 24
		}
		fmt.Fprintln(os.Stdout, os.Getenv("PW_BUN_COMMAND_OUTPUT"))
		return 0
	case "bun2nix":
		if len(os.Args) != 5 || os.Args[1] != "--lock-file" || os.Args[3] != "--output-file" {
			return 25
		}
		if _, err := os.ReadFile(os.Args[2]); err != nil {
			return 26
		}
		if err := os.WriteFile(os.Args[4], []byte("generated expression\n"), 0600); err != nil {
			return 27
		}
		return 0
	default:
		return 28
	}
}

func TestNixBunCLIUsesPinnedExecutableAndReportsFailures(t *testing.T) {
	for _, mode := range []string{"success", "check conflict", "refresh conflict", "ordinary conflict", "bad flag", "help", "missing input", "build failure"} {
		t.Run(mode, func(t *testing.T) {
			repo := t.TempDir()
			bin := filepath.Join(t.TempDir(), "bin")
			converter := t.TempDir()
			testutil.FailErr(t, "create executable directory", os.Mkdir(bin, 0750))
			testutil.FailErr(t, "create converter directory", os.Mkdir(filepath.Join(converter, "bin"), 0750))
			testutil.FailErr(t, "create frontend", os.Mkdir(filepath.Join(repo, "lycaon-den"), 0750))
			testutil.FailErr(t, "create packaging", os.MkdirAll(filepath.Join(repo, "packaging/nix"), 0750))
			testutil.FailErr(t, "write Bun input", os.WriteFile(filepath.Join(repo, "lycaon-den/bun.lock"), []byte("locked packages\n"), 0600))
			flake := `{"nodes":{"bun2nix":{"locked":{"rev":"` + strings.Repeat("a", 40) + `"}}}}`
			testutil.FailErr(t, "write pin", os.WriteFile(filepath.Join(repo, "flake.lock"), []byte(flake), 0600))
			suffix := ""
			if runtime.GOOS == "windows" {
				suffix = ".exe"
			}
			copyBunFixtureExecutable(t, filepath.Join(bin, "nix"+suffix))
			copyBunFixtureExecutable(t, filepath.Join(converter, "bin/bun2nix"+suffix))
			t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
			t.Setenv("PW_BUN_COMMAND_FIXTURE", "1")
			t.Setenv("PW_BUN_COMMAND_OUTPUT", converter)
			args := []string{"--nix-bun", "--repo", repo}
			want := 0
			switch mode {
			case "check conflict":
				args = append(args, "--check")
				want = 2
			case "refresh conflict":
				args = append(args, "--refresh-upstream")
				want = 2
			case "ordinary conflict":
				args = []string{"--check", "--refresh-upstream"}
				want = 2
			case "bad flag":
				args = []string{"--unknown"}
				want = 2
			case "help":
				args = []string{"--help"}
			case "missing input":
				testutil.FailErr(t, "remove input", os.Remove(filepath.Join(repo, "lycaon-den/bun.lock")))
				want = 1
			case "build failure":
				t.Setenv("PW_BUN_COMMAND_FAILURE", "1")
				want = 1
			}
			var stderr bytes.Buffer
			if got := runCLI(args, &stderr); got != want {
				t.Fatalf("CLI exit=%d want=%d stderr=%s", got, want, stderr.String())
			}
			if mode != "success" {
				if stderr.Len() == 0 {
					t.Fatal("CLI omitted its diagnostic or help")
				}
				if _, err := os.Stat(filepath.Join(repo, "packaging/nix/bun-lock.json")); !os.IsNotExist(err) {
					t.Fatalf("invalid invocation published identity: %v", err)
				}
				return
			}
			raw, err := os.ReadFile(filepath.Join(repo, "packaging/nix/bun-lock.json"))
			testutil.FailErr(t, "read published identity", err)
			var identity nixBunIdentity
			testutil.FailErr(t, "decode published identity", json.Unmarshal(raw, &identity))
			if identity.Generator != strings.Repeat("a", 40) || identity.Source == "" || identity.Expression == "" {
				t.Fatalf("CLI published incomplete identity: %+v", identity)
			}
		})
	}
}

func copyBunFixtureExecutable(t *testing.T, destination string) {
	t.Helper()
	executable, err := os.Executable()
	testutil.FailErr(t, "locate fixture executable", err)
	source, err := os.Open(executable)
	testutil.FailErr(t, "open fixture executable", err)
	defer func() { _ = source.Close() }()
	target, err := os.OpenFile(destination, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0700)
	testutil.FailErr(t, "create fixture executable", err)
	_, err = io.Copy(target, source)
	closeErr := target.Close()
	testutil.FailErr(t, "copy fixture executable", err)
	testutil.FailErr(t, "close fixture executable", closeErr)
}
