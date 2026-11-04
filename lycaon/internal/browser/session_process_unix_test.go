//go:build darwin || linux

package browser

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/confine"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestBrowserProcessUsesOwnedNativeDoorInBothPostures(t *testing.T) {
	t.Setenv("LYCAON_SANDBOX", "off")
	confine.TestingSetAutoConfine(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]string{"webSocketDebuggerUrl": "ws://" + r.Host + "/devtools/browser/synthetic-fixture"})
	}))
	defer server.Close()
	t.Setenv("AWS_ACCESS_KEY_ID", "synthetic-browser-credential")
	for _, strict := range []bool{false, true} {
		t.Run(fmt.Sprint(strict), func(t *testing.T) {
			root := t.TempDir()
			entry := filepath.Join(root, "native-browser-fixture")
			script := "#!/bin/sh\nprintf '%s\\n' \"$@\" > \"$0.args\"\nprintf '%s' \"$AWS_ACCESS_KEY_ID\" > \"$0.env\"\nprintf 'DevTools listening on ws://" + strings.TrimPrefix(server.URL, "http://") + "/devtools/browser/synthetic-fixture\\n' >&2\nsleep 30\n"
			testutil.FailErr(t, "write native browser fixture", os.WriteFile(entry, []byte(script), 0700))
			var boundary *confine.Confinement
			if strict {
				boundary = &confine.Confinement{Roots: []string{root}, Network: confine.NetworkDeny, Browser: true, LoopbackConnect: true}
			}
			ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
			defer cancel()
			launcher := newHeadlessLauncher(ctx, entry, root, false, strict)
			url, stop, err := launchBrowserProcess(ctx, launcher, entry, boundary)
			testutil.FailErr(t, "launch owned native browser fixture", err)
			if stop == nil || !strings.HasSuffix(url, "/devtools/browser/synthetic-fixture") {
				t.Fatal("native readiness omitted settled owner")
			}
			stop()
			stop()
			args, err := os.ReadFile(entry + ".args")
			testutil.FailErr(t, "observe actual browser argv", err)
			hasNoSandbox := false
			for _, arg := range strings.Split(string(args), "\n") {
				if arg == "--no-sandbox" {
					hasNoSandbox = true
				}
			}
			if hasNoSandbox != strict {
				t.Fatalf("Chromium sandbox posture changed: strict=%v argv=%q", strict, args)
			}
			environment, err := os.ReadFile(entry + ".env")
			testutil.FailErr(t, "observe actual reduced browser environment", err)
			if len(environment) != 0 {
				t.Fatal("native browser inherited ambient credential")
			}
		})
	}
}

func TestBrowserProcessCancellationSettlesBeforeReturning(t *testing.T) {
	t.Setenv("LYCAON_SANDBOX", "off")
	confine.TestingSetAutoConfine(t)
	root := t.TempDir()
	entry := filepath.Join(root, "native-no-url-browser")
	testutil.FailErr(t, "write blocked native browser", os.WriteFile(entry, []byte("#!/bin/sh\nprintf ready > \"$0.ready\"\nsleep 30\n"), 0700))
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		_, stop, err := launchBrowserProcess(ctx, newHeadlessLauncher(ctx, entry, root, false, false), entry, nil)
		if stop != nil {
			stop()
		}
		done <- err
	}()
	deadline := time.Now().Add(5 * time.Second)
	for {
		_, err := os.Stat(entry + ".ready")
		if err == nil {
			break
		}
		if !errors.Is(err, os.ErrNotExist) || time.Now().After(deadline) {
			t.Fatalf("native fixture did not reach readiness wait: %v", err)
		}
		time.Sleep(10 * time.Millisecond)
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("native browser cancellation lost: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("browser owner returned without bounded settlement")
	}
}
