package exec

import (
	"context"
	"errors"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/testutil"
)

func TestNormalizeWinSizeDefaults(t *testing.T) {
	got := normalizeWinSize(WinSize{})
	if got.Cols != DefaultPTYCols || got.Rows != DefaultPTYRows {
		t.Fatalf("normalizeWinSize(zero) = %+v, want %dx%d", got, DefaultPTYCols, DefaultPTYRows)
	}
	got = normalizeWinSize(WinSize{Cols: 120, Rows: 40})
	if got.Cols != 120 || got.Rows != 40 {
		t.Fatalf("normalizeWinSize(explicit) = %+v", got)
	}
}

func TestApplyDefaultTERM(t *testing.T) {
	got := applyDefaultTERM([]string{"PATH=/bin", "TERM=dumb"}, nil, nil)
	if !envHas(got, "TERM="+DefaultTERM) || envHas(got, "TERM=dumb") {
		t.Fatalf("inherit dumb should be replaced: %v", got)
	}
	keptInline := applyDefaultTERM([]string{"TERM=dumb"}, nil, map[string]string{"TERM": "vt100"})
	// Inline already merged into env by configureCmdEnv in StartPTY; here we only
	// check the early-return when inline carries TERM.
	if keptInline[0] != "TERM=dumb" {
		t.Fatalf("inline TERM present should leave env untouched: %v", keptInline)
	}
	keptEnv := applyDefaultTERM([]string{"TERM=vt100"}, []string{"TERM=vt100"}, nil)
	if !envHas(keptEnv, "TERM=vt100") {
		t.Fatalf("explicit Env TERM should win: %v", keptEnv)
	}
}

func envHas(env []string, entry string) bool {
	for _, e := range env {
		if e == entry {
			return true
		}
	}
	return false
}

func TestStartPTYUnsupportedOnWindows(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("windows-only typed reject")
	}
	_, err := StartPTY(context.Background(), "echo", []string{"hi"}, PTYOpts{Launch: HostLaunch("pty test"),
		Timeout: 5 * time.Second,
	})
	if !errors.Is(err, ErrPTYUnsupported) {
		t.Fatalf("StartPTY err = %v, want ErrPTYUnsupported", err)
	}
}

func TestStartPTYRejectsMetacharName(t *testing.T) {
	_, err := StartPTY(context.Background(), "echo;rm", nil, PTYOpts{Launch: HostLaunch("pty test")})
	if err == nil {
		t.Fatal("expected metachar rejection")
	}
}

func TestStartPTYUnixAllocates(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("unix openpty")
	}
	session, err := StartPTY(context.Background(), "true", nil, PTYOpts{Launch: HostLaunch("pty test"),
		Timeout: 5 * time.Second,
	})
	testutil.FailErr(t, "StartPTY true", err)
	defer func() { _ = session.Close() }()
	if session.Pid() <= 0 {
		t.Fatalf("pid = %d", session.Pid())
	}
	testutil.FailErr(t, "Wait true", session.Wait())
}

func TestRunPTYCapturesMainWithDefaults(t *testing.T) {
	if runtime.GOOS == "windows" {
		_, _, err := RunPTY(context.Background(), "echo", []string{"hi"}, PTYOpts{Launch: HostLaunch("pty test"),
			Timeout: 5 * time.Second,
		})
		if !errors.Is(err, ErrPTYUnsupported) {
			t.Fatalf("RunPTY err = %v, want ErrPTYUnsupported", err)
		}
		return
	}
	out, code, err := RunPTY(context.Background(), "sh", []string{"-c", `printf '%s\n' "$TERM"; stty size`}, PTYOpts{Launch: HostLaunch("pty test"),
		Timeout:        5 * time.Second,
		MaxOutputBytes: 4096,
		// Zero WinSize → default 80×24; ensureTERM injects xterm-256color.
	})
	testutil.FailErr(t, "RunPTY TERM/winsize", err)
	if code != 0 {
		t.Fatalf("exit = %d output=%q", code, out)
	}
	got := string(out)
	if !strings.Contains(got, DefaultTERM) {
		t.Fatalf("output missing default TERM: %q", got)
	}
	if !strings.Contains(got, "24 80") {
		t.Fatalf("output missing default winsize 24 80: %q", got)
	}
}
