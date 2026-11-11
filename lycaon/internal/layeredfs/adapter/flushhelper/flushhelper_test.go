package flushhelper

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"
)

type failingReader struct{}

func (failingReader) Read([]byte) (int, error) { return 0, errors.New("stdin closed") }

// A request round-trips through the helper: it flushes the named paths and replies with its counts.
func TestServeFlushesTheRequestedPathsAndRepliesWithCounts(t *testing.T) {
	var gotRoot string
	var gotPaths []string
	flush := func(root string, paths []string) (int64, int64, error) {
		gotRoot, gotPaths = root, paths
		return 3, 1, nil
	}
	var out, errOut bytes.Buffer
	if code := serve("", bytes.NewReader(Request("/mnt/view", []string{"a.txt", "dir"})), &out, &errOut, flush); code != 0 {
		t.Fatalf("exit %d: %s", code, errOut.String())
	}
	if gotRoot != "/mnt/view" || !reflect.DeepEqual(gotPaths, []string{"a.txt", "dir"}) {
		t.Fatalf("flushed %q %v", gotRoot, gotPaths)
	}
	if files, dirs := Reply(out.Bytes()); files != 3 || dirs != 1 {
		t.Fatalf("reply %q gave files=%d dirs=%d", out.String(), files, dirs)
	}
}

// A failed flush, an unreadable request and a request without a mount root each end with their own status.
func TestServeReportsEachFailure(t *testing.T) {
	failed := func(string, []string) (int64, int64, error) { return 0, 0, errors.New("sync refused") }
	for name, tc := range map[string]struct {
		in     func() *bytes.Reader
		reader bool
		flush  Flush
		code   int
		stderr string
	}{
		"flush fails":  {in: func() *bytes.Reader { return bytes.NewReader(Request("/mnt", nil)) }, flush: failed, code: 1, stderr: "sync refused"},
		"no root":      {in: func() *bytes.Reader { return bytes.NewReader(nil) }, flush: failed, code: 2},
		"stdin closed": {reader: true, flush: failed, code: 1, stderr: "stdin closed"},
	} {
		t.Run(name, func(t *testing.T) {
			var out, errOut bytes.Buffer
			var code int
			if tc.reader {
				code = serve("", failingReader{}, &out, &errOut, tc.flush)
			} else {
				code = serve("", tc.in(), &out, &errOut, tc.flush)
			}
			if code != tc.code || !strings.Contains(errOut.String(), tc.stderr) || out.Len() != 0 {
				t.Fatalf("exit %d stdout %q stderr %q", code, out.String(), errOut.String())
			}
		})
	}
}

// A pause file holds the helper, with its PID written in it, until the file is removed.
func TestServeWaitsOnItsPauseFile(t *testing.T) {
	pause := filepath.Join(t.TempDir(), "pause")
	flushed := make(chan struct{})
	done := make(chan int, 1)
	go func() {
		var out, errOut bytes.Buffer
		done <- serve(pause, bytes.NewReader(Request("/mnt", []string{"."})), &out, &errOut, func(string, []string) (int64, int64, error) {
			close(flushed)
			return 0, 1, nil
		})
	}()
	var pid []byte
	for pid == nil {
		pid, _ = os.ReadFile(pause)
		time.Sleep(time.Millisecond)
	}
	if string(bytes.TrimSpace(pid)) != strconv.Itoa(os.Getpid()) {
		t.Fatalf("pause file holds %q", pid)
	}
	select {
	case <-flushed:
		t.Fatal("flushed while paused")
	default:
	}
	if err := os.Remove(pause); err != nil {
		t.Fatal(err)
	}
	if code := <-done; code != 0 {
		t.Fatalf("exit %d", code)
	}
}
