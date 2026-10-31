//go:build darwin || linux

package command_test

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"net"
	"net/http/httptest"
	"os/user"
	"strconv"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/testutil"
	"golang.org/x/crypto/ssh"
)

func TestProgramSSHReachesPasswordAuthenticationWithoutPrompting(t *testing.T) {
	binary := requiredExternal(t, "ssh")
	_, private, err := ed25519.GenerateKey(rand.Reader)
	testutil.FailErr(t, "generate fixture host key", err)
	signer, err := ssh.NewSignerFromKey(private)
	testutil.FailErr(t, "load fixture host key", err)
	attempts := make(chan string, 8)
	config := &ssh.ServerConfig{
		PasswordCallback: func(ssh.ConnMetadata, []byte) (*ssh.Permissions, error) {
			return nil, errors.New("fixture refuses credentials")
		},
		AuthLogCallback: func(_ ssh.ConnMetadata, method string, _ error) { attempts <- method },
	}
	config.AddHostKey(signer)
	// Reuse the standard fixture's ephemeral loopback listener. No endpoint or
	// credentials are part of the repository fixture.
	listener := httptest.NewUnstartedServer(nil).Listener
	t.Cleanup(func() { _ = listener.Close() })
	served := make(chan error, 1)
	go func() {
		conn, err := listener.Accept()
		if err != nil {
			served <- err
			return
		}
		defer conn.Close()
		_ = conn.SetDeadline(time.Now().Add(10 * time.Second))
		_, _, _, err = ssh.NewServerConn(conn, config)
		served <- err
	}()
	host, port, err := net.SplitHostPort(listener.Addr().String())
	testutil.FailErr(t, "read fixture listener address", err)
	person, err := user.Current()
	testutil.FailErr(t, "read current process identity", err)
	f := newProgramFixture(t)
	portNumber, err := strconv.ParseUint(port, 10, 16)
	testutil.FailErr(t, "parse fixture listener port", err)
	f.Runtime.Executor.Boundary.SetSessionLoopbackGrant(func(context.Context, string, string) (bool, []uint16) {
		return true, []uint16{uint16(portNumber)}
	})
	ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
	defer cancel()
	out, err := f.call(t, ctx, externalProgram(binary, "-F", "/dev/null", "-o", "StrictHostKeyChecking=no", "-o", "UserKnownHostsFile=/dev/null", "-o", "PubkeyAuthentication=no", "-o", "PreferredAuthentications=password", "-o", "ConnectTimeout=3", "-p", port, "-l", person.Username, host, "true"))
	testutil.FailErr(t, "run password-only SSH fixture", err)
	got := decodeOutcome(t, out)
	if got.Status != 255 || got.Stderr == "" || ctx.Err() != nil {
		t.Fatalf("SSH did not settle authentication failure: %+v context=%v", got, ctx.Err())
	}
	// Closing the listener makes a missing connection complete the server goroutine.
	testutil.FailErr(t, "close SSH fixture listener", listener.Close())
	if err := <-served; err == nil || errors.Is(err, net.ErrClosed) {
		t.Fatalf("SSH did not complete authentication with the fixture: %v; stderr=%q", err, got.Stderr)
	}
	select {
	case method := <-attempts:
		if method != "none" && method != "password" {
			t.Fatalf("unexpected authentication method %q", method)
		}
	default:
		t.Fatal("SSH failed before reaching the authentication fixture")
	}
}
