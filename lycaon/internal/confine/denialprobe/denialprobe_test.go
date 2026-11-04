package denialprobe_test

import (
	"bufio"
	"bytes"
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"syscall"
	"testing"

	"github.com/lycaon/lycaon/internal/confine/denialprobe"
	"github.com/lycaon/lycaon/internal/testutil"
)

func serve(t *testing.T, steps ...denialprobe.Step) []denialprobe.Result {
	t.Helper()
	arg, err := denialprobe.Encode(steps)
	testutil.FailErr(t, "encode", err)
	var out bytes.Buffer
	testutil.FailErr(t, "serve", denialprobe.Serve(arg, &out))
	var results []denialprobe.Result
	lines := bufio.NewScanner(&out)
	for lines.Scan() {
		var result denialprobe.Result
		testutil.FailErr(t, "decode", json.Unmarshal(lines.Bytes(), &result))
		results = append(results, result)
	}
	return results
}

func TestServeReportsTheKernelsAnswerForEachStep(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "file")
	testutil.FailErr(t, "write", os.WriteFile(file, []byte("x"), 0o600))
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	testutil.FailErr(t, "listen", err)
	defer func() { _ = listener.Close() }()
	port := uint16(listener.Addr().(*net.TCPAddr).Port) //nolint:gosec // a listener's own port.

	created := filepath.Join(dir, "created")
	results := serve(t,
		denialprobe.Step{Kind: denialprobe.Stat, Path: file},
		denialprobe.Step{Kind: denialprobe.ReadFile, Path: file},
		denialprobe.Step{Kind: denialprobe.CreateFile, Path: created},
		denialprobe.Step{Kind: denialprobe.Connect, Port: port},
	)
	if len(results) != 4 {
		t.Fatalf("results = %+v", results)
	}
	for _, result := range results {
		if !result.Succeeded() || result.Denied() {
			t.Fatalf("%s failed: %+v", result.Kind, result)
		}
	}
	if _, err := os.Lstat(created); !os.IsNotExist(err) {
		t.Fatalf("a created file stayed: %v", err)
	}
}

func TestOnlyABoundarysAnswerCountsAsADenial(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "missing")
	result := serve(t, denialprobe.Step{Kind: denialprobe.ReadFile, Path: missing})[0]
	if result.Errno != int(syscall.ENOENT) || !result.Denied() {
		t.Fatalf("absent path = %+v", result)
	}
	for _, errno := range []syscall.Errno{syscall.EIO, syscall.EINVAL, syscall.ECONNREFUSED, 0} {
		if (denialprobe.Result{Errno: int(errno)}).Denied() {
			t.Fatalf("errno %d counted as a denial", errno)
		}
	}
	if (denialprobe.Result{Errno: -1}).Denied() {
		t.Fatal("an unclassified failure counted as a denial")
	}
	unknown := serve(t, denialprobe.Step{Kind: "unknown"})[0]
	if unknown.Succeeded() || unknown.Denied() {
		t.Fatalf("unknown step = %+v", unknown)
	}
}

func TestInvokedRecognizesOnlyTheExactArguments(t *testing.T) {
	for _, argv := range [][]string{{"pw"}, {"pw", denialprobe.Flag}, {"pw", "other", "[]"}, {"pw", denialprobe.Flag, "[]", "extra"}} {
		if denialprobe.Invoked(argv) {
			t.Fatalf("%v invoked the probe", argv)
		}
	}
	if !denialprobe.Invoked([]string{"pw", denialprobe.Flag, "[]"}) {
		t.Fatal("exact arguments did not invoke the probe")
	}
}

func TestServeRefusesAnUnreadablePlan(t *testing.T) {
	if err := denialprobe.Serve("not json", &bytes.Buffer{}); err == nil {
		t.Fatal("an unreadable plan was served")
	}
}
