package vaultidentity

import (
	"bytes"
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
)

func TestFramesRoundTripAndStayBounded(t *testing.T) {
	var buf bytes.Buffer
	in := Request{Op: OpCreate, Account: "age-x25519-identity-v1:ab", Value: []byte("identity")}
	testutil.FailErr(t, "write frame", WriteFrame(&buf, in))
	var out Request
	testutil.FailErr(t, "read frame", ReadFrame(&buf, &out))
	if out.Op != in.Op || out.Account != in.Account || !bytes.Equal(out.Value, in.Value) {
		t.Fatalf("frame = %+v", out)
	}
	if err := WriteFrame(&buf, Request{Value: make([]byte, maxFrameBytes)}); err == nil {
		t.Fatal("oversized frame accepted")
	}
	if err := ReadFrame(bytes.NewReader([]byte{0, 0, 0, 0}), &out); err == nil {
		t.Fatal("empty frame accepted")
	}
	if err := ReadFrame(bytes.NewReader([]byte{0xff, 0xff, 0xff, 0xff}), &out); err == nil {
		t.Fatal("huge frame accepted")
	}
	if err := ReadFrame(bytes.NewReader([]byte{0, 0, 0, 2, '{'}), &out); !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Fatalf("truncated frame: %v", err)
	}
	if err := ReadFrame(bytes.NewReader([]byte{0, 0, 0, 1, '{'}), &out); err == nil {
		t.Fatal("malformed JSON accepted")
	}
}

func TestAccountsAreSingleSafePathSegments(t *testing.T) {
	for _, ok := range []string{"age-x25519-identity-v1:0a1b", "a", "A.b_c-d:e"} {
		if !ValidAccount(ok) {
			t.Fatalf("%q rejected", ok)
		}
	}
	for _, bad := range []string{"", ".hidden", "..", "a/b", "a b", "a\x00", string(make([]byte, 129))} {
		if ValidAccount(bad) {
			t.Fatalf("%q accepted", bad)
		}
	}
}

func TestResponseErrorsCarryStatus(t *testing.T) {
	if err := (Response{Status: StatusOK}).Err(); err != nil {
		t.Fatalf("ok response errored: %v", err)
	}
	err := (Response{Status: StatusRefused, Code: "executable_mismatch", Message: "pw for pid 7"}).Err()
	if StatusOf(err) != StatusRefused || err.Error() != "vault identity service: refused (executable_mismatch: pw for pid 7)" {
		t.Fatalf("err = %v", err)
	}
	if StatusOf(errors.New("other")) != "" {
		t.Fatal("plain error carries a status")
	}
	if (Response{Status: StatusNotFound, Code: "x"}).Err().Error() != "vault identity service: not_found (x)" ||
		(Response{Status: StatusUnavailable}).Err().Error() != "vault identity service: unavailable" {
		t.Fatal("status error wording changed")
	}
}

// shortSocketPath stays under the Unix socket path cap.
func shortSocketPath(t *testing.T, name string) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "vi") //nolint:usetesting // Short socket path.
	testutil.FailErr(t, "MkdirTemp", err)
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return filepath.Join(dir, name)
}

// echoService answers every request with ok and the request's own value,
// closing the connection after `drop` responses when drop is positive.
func echoService(t *testing.T, drop int) string {
	t.Helper()
	path := shortSocketPath(t, "s.sock")
	listener, err := net.Listen("unix", path)
	testutil.FailErr(t, "listen", err)
	t.Cleanup(func() { listener.Close() })
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			go func() {
				defer conn.Close()
				for served := 0; drop <= 0 || served < drop; served++ {
					var req Request
					if ReadFrame(conn, &req) != nil {
						return
					}
					_ = WriteFrame(conn, Response{Status: StatusOK, Value: req.Value, LauncherPID: 7})
				}
			}()
		}
	}()
	return path
}

func TestClientReconnectsAfterTheServiceClosesAnIdleConnection(t *testing.T) {
	client := NewClient(echoService(t, 1))
	defer client.Close()
	for i := range 3 {
		resp, err := client.Do(Request{Op: OpRead, Value: []byte{byte(i)}})
		testutil.FailErr(t, "request", err)
		if resp.LauncherPID != 7 || !bytes.Equal(resp.Value, []byte{byte(i)}) {
			t.Fatalf("response %d = %+v", i, resp)
		}
	}
	testutil.FailErr(t, "close", client.Close())
	testutil.FailErr(t, "close again", client.Close())
}

func TestClientReportsAnAbsentService(t *testing.T) {
	client := NewClient(shortSocketPath(t, "missing.sock"))
	if _, err := client.Do(Request{Op: OpProbe}); !errors.Is(err, ErrServiceAbsent) {
		t.Fatalf("missing socket: %v", err)
	}
	path := shortSocketPath(t, "stale.sock")
	listener, err := net.Listen("unix", path)
	testutil.FailErr(t, "listen", err)
	listener.Close()
	if _, err := NewClient(path).Do(Request{Op: OpProbe}); !errors.Is(err, ErrServiceAbsent) {
		t.Fatalf("stale socket: %v", err)
	}
}
