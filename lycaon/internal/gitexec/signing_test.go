package gitexec

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/exec"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestSigningRequestedReadsArgvNotOutput(t *testing.T) {
	cases := []struct {
		name        string
		args        []string
		extraConfig []string
		wantSubject signingSubject
		want        bool
	}{
		{name: "plain commit", args: []string{"commit", "-m", "msg"}, wantSubject: signingSubjectCommit},
		{name: "signoff is not signing", args: []string{"commit", "-s", "-m", "msg"}, wantSubject: signingSubjectCommit},
		{name: "untracked-files is not signing", args: []string{"commit", "-u", "-m", "msg"}, wantSubject: signingSubjectCommit},
		{
			name:        "commit -S",
			args:        []string{"commit", "-S", "-m", "msg"},
			wantSubject: signingSubjectCommit,
			want:        true,
		},
		{
			name:        "commit -S with inline key",
			args:        []string{"commit", "-SABCD1234", "-m", "msg"},
			wantSubject: signingSubjectCommit,
			want:        true,
		},
		{
			name:        "commit --gpg-sign=key",
			args:        []string{"commit", "--gpg-sign=ABCD1234"},
			wantSubject: signingSubjectCommit,
			want:        true,
		},
		{
			name:        "explicit --no-gpg-sign wins over a later config toggle",
			args:        []string{"commit", "-S", "--no-gpg-sign"},
			extraConfig: []string{"commit.gpgsign=true"},
			wantSubject: signingSubjectCommit,
		},
		{
			name:        "message body is not a subcommand",
			args:        []string{"commit", "-m", "tag -s the release"},
			wantSubject: signingSubjectCommit,
		},
		{
			name:        "tag -s",
			args:        []string{"tag", "-s", "v1"},
			wantSubject: signingSubjectTag,
			want:        true,
		},
		{
			name:        "tag -u key",
			args:        []string{"tag", "-u", "ABCD", "v1"},
			wantSubject: signingSubjectTag,
			want:        true,
		},
		{name: "plain tag", args: []string{"tag", "v1"}, wantSubject: signingSubjectTag},
		{
			name:        "ExtraConfig lifts the pinned commit toggle",
			args:        []string{"commit", "-m", "msg"},
			extraConfig: []string{"commit.gpgSign=true"},
			wantSubject: signingSubjectCommit,
			want:        true,
		},
		{
			name:        "ExtraConfig false leaves it pinned off",
			args:        []string{"commit", "-m", "msg"},
			extraConfig: []string{"commit.gpgsign=false"},
			wantSubject: signingSubjectCommit,
		},
		{
			name:        "tag toggle does not enable commit signing",
			args:        []string{"commit", "-m", "msg"},
			extraConfig: []string{"tag.gpgsign=true"},
			wantSubject: signingSubjectCommit,
		},
		{name: "status never signs", args: []string{"status", "--porcelain"}},
		{name: "log -S is a pickaxe", args: []string{"log", "-S", "needle"}},
		{
			name:        "global options are skipped before the subcommand",
			args:        []string{"-c", "core.pager=cat", "tag", "--sign", "v1"},
			wantSubject: signingSubjectTag,
			want:        true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			subject, got := signingRequested(tc.args, tc.extraConfig)
			if got != tc.want {
				t.Fatalf("signingRequested(%v, %v) = %v, want %v", tc.args, tc.extraConfig, got, tc.want)
			}
			if subject != tc.wantSubject {
				t.Fatalf("subject = %q, want %q", subject, tc.wantSubject)
			}
		})
	}
}

func TestUnresolvedSigningProgramResolvesFromFormat(t *testing.T) {
	present, err := os.Executable()
	testutil.FailErr(t, "resolve test executable", err)

	cases := []struct {
		name        string
		extraConfig []string
		wantProgram string
		wantMissing bool
	}{
		{
			name:        "openpgp default names gpg",
			extraConfig: nil,
			wantProgram: "gpg",
		},
		{
			name:        "gpg.program is the unqualified openpgp spelling",
			extraConfig: []string{"gpg.program=" + present},
			wantProgram: present,
		},
		{
			name:        "gpg.openpgp.program outranks nothing else",
			extraConfig: []string{"gpg.openpgp.program=" + present},
			wantProgram: present,
		},
		{
			name:        "ssh format defaults to ssh-keygen",
			extraConfig: []string{"gpg.format=ssh", "gpg.ssh.program=" + present},
			wantProgram: present,
		},
		{
			name:        "an unknown format resolves no program",
			extraConfig: []string{"gpg.format=nonsense"},
			wantProgram: "gpg.nonsense.program",
			wantMissing: true,
		},
		{
			name:        "a program that is not on PATH is missing",
			extraConfig: []string{"gpg.program=lycaon-no-such-signing-program"},
			wantProgram: "lycaon-no-such-signing-program",
			wantMissing: true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			program, missing := unresolvedSigningProgram(tc.extraConfig)
			if program != tc.wantProgram {
				t.Fatalf("program = %q, want %q", program, tc.wantProgram)
			}
			if tc.wantMissing && !missing {
				t.Fatalf("program %q resolved, want missing", program)
			}
			if !tc.wantMissing && missing {
				t.Skipf("host has no %q on PATH; capability check is host-dependent", program)
			}
		})
	}
}

func TestSigningUnsupportedNeedsBothFacts(t *testing.T) {
	missingProgram := []string{"gpg.program=lycaon-no-such-signing-program"}

	if err := signingUnsupported([]string{"commit", "-m", "msg"}, Opts{ExtraConfig: missingProgram}); err != nil {
		t.Fatalf("no signature requested, got %v", err)
	}
	err := signingUnsupported([]string{"commit", "-S", "-m", "msg"}, Opts{ExtraConfig: missingProgram})
	if err == nil {
		t.Fatal("requested signature with no signing program should be unsupported")
	}
	if !strings.Contains(err.Detail, string(signingSubjectCommit)) {
		t.Fatalf("Detail = %q, want the signed object named", err.Detail)
	}
	if err.Code() != "GIT_SIGNING_UNSUPPORTED" {
		t.Fatalf("Code() = %q", err.Code())
	}
}

// The signing program resolves on the PATH git runs with, not the engine's
// process PATH.
func TestUnresolvedSigningProgramReadsTheResolvedPath(t *testing.T) {
	resolved := t.TempDir()
	testutil.FailErr(t, "write program", os.WriteFile(filepath.Join(resolved, "resolved-signer"), []byte("#!/bin/sh\n"), 0o755))
	t.Setenv("PATH", t.TempDir())
	exec.SetResolvedPathSource(func() string { return resolved })
	t.Cleanup(func() { exec.SetResolvedPathSource(nil) })

	if program, missing := unresolvedSigningProgram([]string{"gpg.program=resolved-signer"}); missing {
		t.Fatalf("program %q missing, want it found on the resolved PATH", program)
	}
}
