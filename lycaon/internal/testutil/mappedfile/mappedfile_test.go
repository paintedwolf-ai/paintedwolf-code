//go:build linux || darwin

package mappedfile

import (
	"bufio"
	"bytes"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
)

func TestMain(m *testing.M) {
	ServeIfChild()
	os.Exit(m.Run())
}

func TestWritesReachTheFileOnceSynced(t *testing.T) {
	path := filepath.Join(t.TempDir(), "mapped")
	if err := os.WriteFile(path, bytes.Repeat([]byte("0"), 8), 0o600); err != nil {
		testutil.FailErr(t, "write file", err)
	}
	m := Map(t, path, 8)
	if err := m.Write([]byte("mapped!!")); err != nil {
		t.Fatalf("write: %v", err)
	}
	if err := m.Sync(); err != nil {
		t.Fatalf("sync: %v", err)
	}
	got, err := os.ReadFile(path)
	if err != nil || string(got) != "mapped!!" {
		t.Fatalf("file = %q, %v", got, err)
	}
}

func TestDataLongerThanTheMappingIsRefused(t *testing.T) {
	path := filepath.Join(t.TempDir(), "mapped")
	if err := os.WriteFile(path, []byte("0123"), 0o600); err != nil {
		testutil.FailErr(t, "write file", err)
	}
	m := Map(t, path, 4)
	if err := m.Write([]byte("too long")); err == nil || !strings.Contains(err.Error(), "do not fit") {
		t.Fatalf("oversized write = %v, want a refusal", err)
	}
	if err := m.Write([]byte("ok")); err != nil {
		t.Fatalf("the child keeps serving after a refusal: %v", err)
	}
}

func TestChildRefusalsAndExitReachTheCaller(t *testing.T) {
	var out bytes.Buffer
	if code := serve(filepath.Join(t.TempDir(), "missing"), 4, strings.NewReader(""), &out); code != 1 || !strings.HasPrefix(out.String(), "error ") {
		t.Fatalf("missing file: code %d, reply %q", code, out.String())
	}
	if err := write(make([]byte, 4), "zz"); err == nil {
		t.Fatal("malformed data was accepted")
	}

	missing := &recorder{TB: t}
	runRecorded(func() { Map(missing, filepath.Join(t.TempDir(), "missing"), 4) })
	if !strings.Contains(missing.message, "map ") {
		t.Fatalf("mapping a missing file reported %q", missing.message)
	}

	path := filepath.Join(t.TempDir(), "mapped")
	if err := os.WriteFile(path, []byte("0123"), 0o600); err != nil {
		testutil.FailErr(t, "write file", err)
	}
	m := Map(t, path, 4)
	m.close()
	if err := m.Write([]byte("x")); err == nil {
		t.Fatal("a write after the child exited succeeded")
	}
	ended := &File{replies: bufio.NewScanner(strings.NewReader(""))}
	if err := ended.await(); err == nil || !strings.Contains(err.Error(), "exited") {
		t.Fatalf("an ended reply stream = %v", err)
	}
}

// recorder ends the calling goroutine on Fatalf and keeps the message.
type recorder struct {
	testing.TB
	message string
}

func (r *recorder) Helper() {}

func (r *recorder) Fatalf(format string, args ...any) {
	r.message = fmt.Sprintf(format, args...)
	runtime.Goexit()
}

func runRecorded(fn func()) {
	done := make(chan struct{})
	go func() {
		defer close(done)
		fn()
	}()
	<-done
}

func TestServeAnswersEachCommandAndExitsAtEndOfInput(t *testing.T) {
	path := filepath.Join(t.TempDir(), "mapped")
	if err := os.WriteFile(path, bytes.Repeat([]byte("0"), 8), 0o600); err != nil {
		testutil.FailErr(t, "write file", err)
	}
	commands := strings.NewReader(hex.EncodeToString([]byte("mapped!!")) + "\nzz\nsync\n")
	var replies bytes.Buffer
	if code := serve(path, 8, commands, &replies); code != 0 {
		t.Fatalf("serve exited %d: %q", code, replies.String())
	}
	lines := strings.Split(strings.TrimSpace(replies.String()), "\n")
	if len(lines) != 4 || lines[0] != "ok" || lines[1] != "ok" || !strings.HasPrefix(lines[2], "error ") || lines[3] != "ok" {
		t.Fatalf("replies = %q, want ready, write, refusal, sync", lines)
	}
	if got, err := os.ReadFile(path); err != nil || string(got) != "mapped!!" {
		t.Fatalf("file = %q, %v", got, err)
	}
}
