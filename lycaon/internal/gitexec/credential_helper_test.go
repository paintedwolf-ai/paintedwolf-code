package gitexec

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
)

func TestCredentialHelperResolutionPerPlatform(t *testing.T) {
	executablePath = func() (string, error) { return "/opt/painted-wolf-code/pw", nil }
	t.Cleanup(func() { executablePath = os.Executable })
	vault := "!'/opt/painted-wolf-code/pw' git-credential-vault"
	keychain := "!'/opt/painted-wolf-code/pw' git-credential-osxkeychain"
	for _, tc := range []struct {
		goos, helper, want string
		served             hostHelper
	}{
		{"linux", "", vault, hostHelperVault},
		{"linux", "store", vault, hostHelperVault},
		{"linux", " store ", vault, hostHelperVault},
		{"linux", "cache --timeout=900", "cache --timeout=900", hostHelperNone},
		{"linux", "/usr/lib/git-core/git-credential-libsecret", "/usr/lib/git-core/git-credential-libsecret", hostHelperNone},
		{"linux", "osxkeychain", keychain, hostHelperKeychain},
		{"darwin", "", "", hostHelperNone},
		{"darwin", "store", "store", hostHelperNone},
		{"darwin", "osxkeychain", keychain, hostHelperKeychain},
		{"windows", "", "", hostHelperNone},
		{"windows", "manager", "manager", hostHelperNone},
	} {
		got, served, err := resolveCredentialHelperFor(tc.helper, tc.goos)
		testutil.FailErr(t, tc.goos+" "+tc.helper, err)
		if got != tc.want || served != tc.served {
			t.Fatalf("%s helper %q = %q (%q), want %q (%q)", tc.goos, tc.helper, got, served, tc.want, tc.served)
		}
	}
	executablePath = func() (string, error) { return "", errors.New("unknown executable") }
	if _, _, err := resolveCredentialHelperFor("", "linux"); err == nil {
		t.Fatal("host helper resolved without an executable path")
	}
	if got, _, err := resolveCredentialHelperFor("manager", "linux"); err != nil || got != "manager" {
		t.Fatalf("user helper = %q, %v", got, err)
	}
}

func TestNetworkInvocationServesTheVaultHelperOnLinux(t *testing.T) {
	useBundledGit(t)
	SetHostConfig(stubHostConfig{helper: ""})
	executablePath = func() (string, error) { return "/opt/painted-wolf-code/pw", nil }
	argv, network, err := buildArgv(t.Context(), t.TempDir(), Opts{Profile: ProfileNetwork, RemoteURL: "https://example.com/r.git"}, []string{"fetch"})
	testutil.FailErr(t, "buildArgv", err)
	wantVault := strings.Contains(strings.Join(argv, "\x00"), "credential.helper=!'/opt/painted-wolf-code/pw' git-credential-vault")
	if (network.helper == hostHelperVault) != wantVault || network.remote != "https://example.com/r.git" {
		t.Fatalf("network facts %+v with argv %q", network, argv)
	}
	started, stopped, bound := 0, 0, 0
	previous := credentialServiceFn
	credentialServiceFn = func(context.Context, string) (credentialService, error) {
		started++
		return credentialService{
			socket: "/run/user/1000/pw-git-x/credential",
			bind: func(pid int) error {
				if pid > 0 {
					bound++
				}
				return nil
			},
			stop: func() error { stopped++; return nil },
		}, nil
	}
	t.Cleanup(func() { credentialServiceFn = previous })
	dir := t.TempDir()
	initRepo(t, dir)
	out, code, err := Run(t.Context(), dir, []string{"config", "--get-all", "credential.helper"}, Opts{Profile: ProfileNetwork, RemoteURL: "https://example.com/r.git"})
	if err != nil || code != 0 {
		t.Fatalf("network invocation: err=%v code=%d out=%s", err, code, out)
	}
	if wantVault && (started != 1 || stopped != 1 || bound != 1) {
		t.Fatalf("credential service started %d times, bound %d times and stopped %d times", started, bound, stopped)
	}
	if !wantVault && started != 0 {
		t.Fatal("credential service started where no vault helper is served")
	}
	if !wantVault {
		return
	}
	credentialServiceFn = func(context.Context, string) (credentialService, error) {
		return credentialService{}, errors.New("socket directory unavailable")
	}
	if _, _, err := Run(t.Context(), dir, []string{"config", "--get-all", "credential.helper"}, Opts{Profile: ProfileNetwork, RemoteURL: "https://example.com/r.git"}); err == nil || !strings.Contains(err.Error(), "socket directory unavailable") {
		t.Fatalf("service failure = %v", err)
	}
}
