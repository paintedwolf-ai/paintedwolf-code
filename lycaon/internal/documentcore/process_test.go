package documentcore

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
)

func TestCoreExitClosesTheEngineAndSaysHow(t *testing.T) {
	engine := coreForTest(t)
	callForTest(t, engine, Request{Action: "open", Handle: 1, Client: 1})
	testutil.FailErr(t, "kill core", engine.proc.cmd.Process.Kill())
	<-engine.proc.done

	_, err := engine.Call(t.Context(), Request{Action: "inspect", Handle: 1})
	var exit *ExitError
	if !errors.As(err, &exit) || !errors.Is(err, ErrExited) {
		t.Fatalf("call after the core died = %v, want an ExitError", err)
	}
	if !strings.Contains(exit.Status, "killed") {
		t.Fatalf("exit status = %q, want the signal that ended it", exit.Status)
	}
	if !engine.Closed() {
		t.Fatal("engine still reports open after its process ended")
	}
}

func TestClosingTheEngineIsNotReportedAsACrash(t *testing.T) {
	engine, err := New(t.Context())
	testutil.FailErr(t, "start core", err)
	testutil.FailErr(t, "close core", engine.Close(t.Context()))
	_, err = engine.Call(t.Context(), Request{Action: "open", Handle: 1, Client: 1})
	var exit *ExitError
	if !errors.Is(err, ErrExited) || errors.As(err, &exit) {
		t.Fatalf("call after close = %v, want ErrExited without a crash report", err)
	}
}

func TestVerifyRoundTripsADocument(t *testing.T) {
	verified, err := Verify(t.Context())
	testutil.FailErr(t, "verify core", err)
	if verified.Binary == "" {
		t.Fatal("verification did not name the core it ran")
	}
}

// The ceiling is the process's: a document past it ends the core, never the host.
func TestMemoryCeilingEndsOnlyTheCore(t *testing.T) {
	core, err := Binary()
	testutil.FailErr(t, "resolve core", err)
	var input bytes.Buffer
	for _, request := range []Request{
		{Action: "open", Handle: 1, Client: 1},
		{Action: "edit", Handle: 1, Edits: []Edit{{Insert: strings.Repeat("a", 6<<20)}}},
	} {
		raw, err := json.Marshal(request)
		testutil.FailErr(t, "encode request", err)
		testutil.FailErr(t, "frame request", writeFrame(&input, raw))
	}
	cmd := exec.CommandContext(t.Context(), core, "serve", "--memory-limit-bytes", strconv.Itoa(8<<20))
	cmd.Stdin = &input
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	err = cmd.Run()
	var exit *exec.ExitError
	if !errors.As(err, &exit) || exit.Success() {
		t.Fatalf("core past its ceiling = %v, want it to end abnormally", err)
	}
	if !strings.Contains(stderr.String(), "memory allocation") {
		t.Fatalf("core stderr = %q, want the refused allocation", stderr.String())
	}
}

func TestCoreRefusesMalformedFraming(t *testing.T) {
	core, err := Binary()
	testutil.FailErr(t, "resolve core", err)
	for name, input := range map[string][]byte{
		"oversized frame":   binary.LittleEndian.AppendUint32(nil, maxRequestBytes+1),
		"truncated header":  {1, 0},
		"truncated payload": append(binary.LittleEndian.AppendUint32(nil, 10), '{'),
	} {
		t.Run(name, func(t *testing.T) {
			cmd := exec.CommandContext(context.Background(), core, "serve", "--memory-limit-bytes", strconv.Itoa(memoryLimitBytes))
			cmd.Stdin = bytes.NewReader(input)
			out, err := cmd.Output()
			var exit *exec.ExitError
			if !errors.As(err, &exit) || exit.ExitCode() != 2 {
				t.Fatalf("core on %s = %v, want protocol exit 2", name, err)
			}
			if hello, err := readFrame(bytes.NewReader(out)); err != nil || string(hello) != `{"protocol":1}` {
				t.Fatalf("core did not announce its protocol first: %q, %v", hello, err)
			}
		})
	}
}

func TestHostAndCoreDeclareOneProtocol(t *testing.T) {
	source, err := os.ReadFile(filepath.Join("native", "src", "main.rs"))
	testutil.FailErr(t, "read core source", err)
	declared := regexp.MustCompile(`(?m)^const PROTOCOL: u32 = (\d+);$`).FindSubmatch(source)
	if declared == nil {
		t.Fatal("native/src/main.rs no longer declares const PROTOCOL")
	}
	if string(declared[1]) != strconv.Itoa(protocolVersion) {
		t.Fatalf("core declares protocol %s, host requires %d", declared[1], protocolVersion)
	}
}
