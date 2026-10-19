//go:build linux && nixworkflow

package confine_test

import (
	"encoding/pem"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"sync/atomic"
	"testing"

	"github.com/lycaon/lycaon/internal/confine"
	execpkg "github.com/lycaon/lycaon/internal/exec"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestLinuxNixTLSCacheRequiresGrantedEgressAndVerifiedCertificate(t *testing.T) {
	fixture := prepareNixWorkflows(t)
	var requests atomic.Int64
	cache := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		if r.URL.Path != "/nix-cache-info" {
			http.NotFound(w, r)
			return
		}
		_, _ = io.WriteString(w, "StoreDir: /nix/store\nWantMassQuery: 1\nPriority: 30\n")
	}))
	cache.Config.ErrorLog = log.New(io.Discard, "", 0)
	cache.StartTLS()
	t.Cleanup(cache.Close)
	certificate := filepath.Join(fixture.project, "cache-ca.pem")
	testutil.FailErr(t, "write fixture cache certificate", os.WriteFile(certificate, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: cache.Certificate().Raw}), 0o600))
	parsed, err := url.Parse(cache.URL)
	testutil.FailErr(t, "parse local TLS cache endpoint", err)
	port, err := strconv.ParseUint(parsed.Port(), 10, 16)
	testutil.FailErr(t, "parse bound local TLS port", err)
	args := []string{"--option", "download-attempts", "1", "--option", "connect-timeout", "1", "--option", "ssl-ca-cert-file", certificate, "--store", cache.URL, "store", "info", "--json"}
	result, err := fixture.run(t, t.Context(), execpkg.LaunchAgentCommand, fixture.bin, args...)
	if err == nil && result != nil && result.ExitCode == 0 || requests.Load() != 0 {
		t.Fatal("TLS cache access bypassed the client's denied egress")
	}
	fixture.boundary.LoopbackConnect, fixture.boundary.LoopbackConnectPorts = true, []uint16{uint16(port)}
	result, err = fixture.run(t, t.Context(), execpkg.LaunchAgentCommand, fixture.bin, args...)
	nixResult(t, result, err)
	if requests.Load() == 0 {
		t.Fatal("granted TLS cache control did not reach the server")
	}
	badCertificate := filepath.Join(fixture.project, "wrong-ca.pem")
	testutil.FailErr(t, "write invalid cache trust control", os.WriteFile(badCertificate, []byte("not a certificate"), 0o600))
	args[8] = badCertificate
	result, err = fixture.run(t, t.Context(), execpkg.LaunchAgentCommand, fixture.bin, args...)
	if err == nil && result != nil && result.ExitCode == 0 {
		t.Fatal("granted cache access bypassed TLS verification")
	}
}

func TestLinuxNixSelectedSSHAgentStillRequiresSocketGrant(t *testing.T) {
	fixture := prepareNixWorkflows(t)
	agent := filepath.Join(shortTempDir(t), "agent.sock")
	listener, err := net.Listen("unix", agent)
	testutil.FailErr(t, "create fixture agent endpoint", err)
	t.Cleanup(func() { _ = listener.Close() })
	fixture.env = append(fixture.env, "SSH_AUTH_SOCK="+agent)
	const connect = `import os, socket; s=socket.socket(socket.AF_UNIX); s.connect(os.environ['SSH_AUTH_SOCK']); s.close(); print('agent-connected')`
	args := []string{"--offline", "--store", fixture.store, "shell", filepath.Dir(filepath.Dir(fixture.bash)), "--command", "/usr/bin/python3", "-I", "-c", connect}
	result, err := fixture.run(t, t.Context(), execpkg.LaunchAgentCommand, fixture.bin, args...)
	if err == nil && result != nil && result.ExitCode == 0 {
		t.Fatal("selected shell inferred socket authority from SSH_AUTH_SOCK")
	}
	fixture.boundary.SocketGrants = []confine.SocketGrant{{ApprovedPath: agent, ResolvedPath: agent}}
	result, err = fixture.run(t, t.Context(), execpkg.LaunchAgentCommand, fixture.bin, args...)
	if got := string(nixResult(t, result, err)); got != "agent-connected" {
		t.Fatalf("granted selected shell agent control=%q", got)
	}
}
