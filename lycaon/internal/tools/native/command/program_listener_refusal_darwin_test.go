//go:build darwin

package command_test

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/confine"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/tools"
)

// A listener the sandbox refuses reaches the command's observation with the
// listen recovery, beside whatever else the program probed.
func TestProgramRefusedListenerCarriesItsBind(t *testing.T) {
	if testing.Short() || !confine.Available() {
		t.Skip("darwin + seatbelt sandbox required")
	}
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("python3 is required to open a TCP listener")
	}
	confine.StartRefusalWatch(t.Context(), "")
	ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
	defer cancel()
	for !confine.RefusalWatchLive() {
		if ctx.Err() != nil {
			t.Skip("kernel sandbox reports are not readable on this host")
		}
		time.Sleep(50 * time.Millisecond)
	}
	f := newProgramFixture(t)
	var effects tools.ToolInvocationOut
	f.Configure = func(tc *tools.ToolContext) {
		effects = tools.ToolInvocationOut{}
		tc.Effects.Out = &effects
	}
	out, err := f.call(t, t.Context(), "external -- "+python+" -m http.server 18798 --bind 0.0.0.0")
	testutil.FailErr(t, "run refused listener", err)
	if outcome := decodeOutcome(t, out); outcome.Status == 0 {
		t.Fatalf("refused listener exited 0: %+v", outcome)
	}
	observed := effects.Facts.Confine
	for _, refusal := range observed.Refusals.Refusals {
		t.Logf("refusal %s %q recovery=%s", refusal.Operation, refusal.Target, refusal.Recovery)
	}
	t.Logf("codes=%v witness=%s failed=%v", effects.Facts.Codes, observed.Refusals.Witness, observed.FailedStages)
	var bind bool
	for _, refusal := range observed.Refusals.Refusals {
		bind = bind || (refusal.Operation == "network-bind" && refusal.Recovery == confine.RecoverLocalListen && refusal.Port() == "18798")
	}
	if !bind {
		t.Fatalf("the refused bind did not reach the observation: %+v", observed.Refusals.Refusals)
	}
}

func TestProgramRefusedListenerVariants(t *testing.T) {
	if testing.Short() || !confine.Available() {
		t.Skip("darwin + seatbelt sandbox required")
	}
	confine.StartRefusalWatch(t.Context(), "")
	ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
	defer cancel()
	for !confine.RefusalWatchLive() {
		if ctx.Err() != nil {
			t.Skip("kernel sandbox reports are not readable on this host")
		}
		time.Sleep(50 * time.Millisecond)
	}
	f := newProgramFixture(t)
	var effects tools.ToolInvocationOut
	f.Configure = func(tc *tools.ToolContext) {
		effects = tools.ToolInvocationOut{}
		tc.Effects.Out = &effects
	}
	for _, source := range []string{
		`external -- python3 -c "import socket; s=socket.socket(); s.bind(('0.0.0.0', 18797))"`,
		`external -- /usr/bin/nc -l 18796`,
		`external -- /bin/sh -c "python3 -m http.server 18795 --bind 0.0.0.0; sleep 1"`,
		`external -- /usr/bin/python3 -c "import socket; s=socket.socket(); s.bind(('0.0.0.0', 18794))"`,
		`external -- /opt/homebrew/bin/python3 -c "import socket; s=socket.socket(); s.bind(('127.0.0.1', 18793))"`,
		`external -- /usr/bin/env -i /opt/homebrew/bin/python3 -c "import socket; s=socket.socket(); s.bind(('127.0.0.1', 18792))"`,
		`external -- /opt/homebrew/Cellar/python@3.15/3.15.2/Frameworks/Python.framework/Versions/3.15/Resources/Python.app/Contents/MacOS/Python -c "import socket; s=socket.socket(); s.bind(('127.0.0.1', 18790))"`,
		`external -- /opt/homebrew/bin/python3 -c "import os; print(sorted(os.environ))"`,
	} {
		out, err := f.call(t, t.Context(), source)
		testutil.FailErr(t, "run variant", err)
		outcome := decodeOutcome(t, out)
		t.Logf("%s => status %d witness=%s stderr=%q", source, outcome.Status, effects.Facts.Confine.Refusals.Witness, outcome.Stderr)
		for _, refusal := range effects.Facts.Confine.Refusals.Refusals {
			t.Logf("   refusal %s %q recovery=%s process=%s", refusal.Operation, refusal.Target, refusal.Recovery, refusal.Process)
		}
	}
}

// Logs the profile a command launch applies, for comparison with the bare one.
func TestProgramListenerProfile(t *testing.T) {
	if testing.Short() || !confine.Available() {
		t.Skip("darwin + seatbelt sandbox required")
	}
	f := newProgramFixture(t)
	var captured tools.ToolContext
	f.Configure = func(tc *tools.ToolContext) { captured = *tc }
	_, err := f.call(t, t.Context(), "true")
	testutil.FailErr(t, "capture tool context", err)
	request, reject := tools.ConfineRequestForSpawn(t.Context(), captured, nil)
	if reject != nil {
		t.Fatalf("confine request: %v", reject)
	}
	c, applied := confine.DefaultConfinement(request)
	if !applied {
		t.Fatal("default confinement unavailable")
	}
	profile, err := confine.BuildSeatbeltProfile(*c)
	testutil.FailErr(t, "render profile", err)
	bare, err := confine.BuildSeatbeltProfile(confine.Confinement{Roots: c.Roots, Network: confine.NetworkDeny})
	testutil.FailErr(t, "render bare profile", err)
	t.Logf("PROFILE-BEGIN command\n%s\nPROFILE-END", profile)
	t.Logf("PROFILE-BEGIN bare\n%s\nPROFILE-END", bare)
}

// The same Homebrew bind launched by the bare confined command in this process.
func TestProgramListenerBareLaunchInThisProcess(t *testing.T) {
	if testing.Short() || !confine.Available() {
		t.Skip("darwin + seatbelt sandbox required")
	}
	confine.StartRefusalWatch(t.Context(), "")
	ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
	defer cancel()
	for !confine.RefusalWatchLive() {
		if ctx.Err() != nil {
			t.Skip("kernel sandbox reports are not readable on this host")
		}
		time.Sleep(50 * time.Millisecond)
	}
	self, err := os.Executable()
	testutil.FailErr(t, "self", err)
	minimal := filepath.Join(t.TempDir(), "helpermain")
	build := exec.Command("go", "build", "-tags", "confinehelpermain", "-o", minimal, "github.com/lycaon/lycaon/internal/confine/testdata/helpermain")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build minimal helper: %v\n%s", err, out)
	}
	for _, helper := range []string{self, minimal} {
		c := confine.Confinement{Roots: []string{t.TempDir()}, Network: confine.NetworkDeny}
		lease, err := confine.BindAction(&c, confine.EgressCommand{SessionID: "bare", ToolName: "command", CommandLine: "python3"})
		testutil.FailErr(t, "bind action", err)
		cmd, cleanup, err := confine.Command(context.Background(), helper, "/opt/homebrew/bin/python3", []string{"-c", "import socket; s=socket.socket(); s.bind(('127.0.0.1', 18788))"}, c)
		testutil.FailErr(t, "command", err)
		out, _ := cmd.CombinedOutput()
		cleanup()
		refusals := lease.SettledRefusals(t.Context())
		lease.Close(t.Context())
		t.Logf("helper %s: refusals %+v output %s", filepath.Base(helper), refusals.Refusals, out)
	}
}
