//go:build linux

package userpath

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

func accountLookupProgram(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "getent")
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"+body+"\n"), 0700); err != nil {
		t.Fatalf("write lookup fixture: %v", err)
	}
	return path
}

func TestLinuxAccountLookupUsesNSSArgumentsAndReducedEnvironment(t *testing.T) {
	t.Setenv("PW_NSS_SECRET", "must not reach lookup")
	program := accountLookupProgram(t, `test "$#" = 2 && test "$1" = passwd && test "$2" = 42 && test -z "$PW_NSS_SECRET" || exit 1
printf 'person:x:42:42:Person:/home/person:/bin/bash\n'`)
	shell, err := linuxAccountShell(t.Context(), program, 42)
	if err != nil || shell != "/bin/bash" {
		t.Fatalf("account shell = %q, %v", shell, err)
	}
}

func TestLinuxAccountLookupRefusesMalformedAndMismatchedRecords(t *testing.T) {
	for _, raw := range []string{
		"", "person:x:42:42:Person:/home/person", "person:x:41:42:Person:/home/person:/bin/bash\n",
		"person:x:not-a-uid:42:Person:/home/person:/bin/bash\n", "person:x:42:42:Person:/home/person:bash\n",
		"person:x:42:42:Person:/home/person:\n", ":x:42:42:Person:/home/person:/bin/bash\n",
		"person:x:42:42:Person:/home/person:/bin/bash\nother:x:42:42:Other:/home/other:/bin/zsh\n",
		"person:x:42:42:Person:/home/person:/bin/bash\x00", "person:x:42:42:Person:/home/person:/bin/bash\r\n",
	} {
		if _, err := linuxAccountRecordShell(raw, 42); err == nil {
			t.Fatalf("accepted malformed record %q", raw)
		}
	}
}

func TestLinuxAccountLookupBoundsCaptureAndLifetime(t *testing.T) {
	oversized := accountLookupProgram(t, "printf '%s' '"+strings.Repeat("x", (64<<10)+1)+"'")
	if _, err := linuxAccountShell(t.Context(), oversized, 42); err == nil {
		t.Fatal("accepted oversized account lookup")
	}
	failed := accountLookupProgram(t, "exit 2")
	if _, err := linuxAccountShell(t.Context(), failed, 42); err == nil {
		t.Fatal("accepted missing account")
	}
	if _, err := linuxAccountShell(t.Context(), filepath.Join(t.TempDir(), "missing"), 42); err == nil {
		t.Fatal("accepted unavailable NSS utility")
	}
	blocked := accountLookupProgram(t, "exec /bin/sleep 10")
	ctx, cancel := context.WithTimeout(t.Context(), 50*time.Millisecond)
	defer cancel()
	start := time.Now()
	if _, err := linuxAccountShell(ctx, blocked, 42); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("blocked lookup error = %v", err)
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Fatalf("lookup cancellation took %s", elapsed)
	}
}

func TestLinuxNativeAccountLookupDoesNotRequireShellEnvironment(t *testing.T) {
	t.Setenv("SHELL", "")
	shell, err := defaultAccountShell(t.Context())
	if err != nil || !filepath.IsAbs(shell) {
		t.Fatalf("native uid %s shell = %q, %v", strconv.Itoa(os.Getuid()), shell, err)
	}
	provider := NewProvider(testConfig())
	got, err := provider.loginShell(t.Context())
	if err != nil || got != shell {
		t.Fatalf("desktop login shell = %q, %v; want %q", got, err, shell)
	}
}
