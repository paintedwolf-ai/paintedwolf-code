package credentialstore

import (
	"errors"
	"net"
	"os"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/vaultidentity"
)

// fakeService answers like the identity service, keeping one account map.
type fakeService struct {
	t      *testing.T
	probe  vaultidentity.Response
	values map[string][]byte
}

func startFakeService(t *testing.T, probe vaultidentity.Response) (*fakeService, *vaultidentity.Client) {
	t.Helper()
	dir, err := os.MkdirTemp("", "vi") //nolint:usetesting // Short socket path.
	testutil.FailErr(t, "MkdirTemp", err)
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	path := filepath.Join(dir, "s.sock")
	listener, err := net.Listen("unix", path)
	testutil.FailErr(t, "listen", err)
	t.Cleanup(func() { _ = listener.Close() })
	svc := &fakeService{t: t, probe: probe, values: map[string][]byte{}}
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			go svc.serve(conn)
		}
	}()
	client := vaultidentity.NewClient(path)
	t.Cleanup(func() { _ = client.Close() })
	return svc, client
}

func (s *fakeService) serve(conn net.Conn) {
	defer conn.Close()
	for {
		var req vaultidentity.Request
		if vaultidentity.ReadFrame(conn, &req) != nil {
			return
		}
		resp := s.probe
		if resp.Status == vaultidentity.StatusOK {
			switch req.Op {
			case vaultidentity.OpProbe:
			case vaultidentity.OpRead:
				if value, ok := s.values[req.Account]; ok {
					resp.Value = value
				} else {
					resp.Status = vaultidentity.StatusNotFound
				}
			case vaultidentity.OpCreate:
				if _, ok := s.values[req.Account]; ok {
					resp.Status = vaultidentity.StatusExists
				} else {
					s.values[req.Account] = append([]byte(nil), req.Value...)
				}
			case vaultidentity.OpRemove:
				delete(s.values, req.Account)
			}
		}
		if vaultidentity.WriteFrame(conn, resp) != nil {
			return
		}
	}
}

func TestServiceCustodyOpensTheVaultUnattended(t *testing.T) {
	svc, client := startFakeService(t, vaultidentity.Response{Status: vaultidentity.StatusOK, LauncherPID: 42})
	c := probeCustody(client)
	if c.mode != custodyService || c.launcherPID != 42 {
		t.Fatalf("custody = %+v", c)
	}
	p := c.provider("/cfg/credential-vault.age").(*serviceHostProvider)
	created, err := p.LoadOrCreate(false)
	testutil.FailErr(t, "create identity", err)
	loaded, err := p.LoadOrCreate(true)
	testutil.FailErr(t, "load identity", err)
	if created != loaded || len(svc.values) != 1 {
		t.Fatal("the service did not keep one identity")
	}
	other := c.provider("/other/credential-vault.age").(*serviceHostProvider)
	if _, err := other.LoadOrCreate(true); !errors.Is(err, ErrVaultCorrupt) {
		t.Fatalf("another root's missing identity: %v", err)
	}
	testutil.FailErr(t, "purge identity", p.Purge())
	if len(svc.values) != 0 {
		t.Fatal("purge left the identity")
	}
	testutil.FailErr(t, "purge again", p.Purge())
	if _, err := p.LoadOrCreate(true); !errors.Is(err, ErrVaultCorrupt) {
		t.Fatalf("missing identity under a vault: %v", err)
	}
}

func TestServiceCustodyReportsEmptyAndFailedResponses(t *testing.T) {
	svc, client := startFakeService(t, vaultidentity.Response{Status: vaultidentity.StatusOK, LauncherPID: 42})
	svc.values[identityAccount("/cfg/credential-vault.age")] = []byte{}
	p := probeCustody(client).provider("/cfg/credential-vault.age")
	if _, err := p.LoadOrCreate(true); !errors.Is(err, ErrVaultCorrupt) {
		t.Fatalf("empty identity: %v", err)
	}
	svc.probe = vaultidentity.Response{Status: vaultidentity.StatusUnavailable, Code: "store"}
	if _, err := p.LoadOrCreate(true); vaultidentity.StatusOf(err) != vaultidentity.StatusUnavailable {
		t.Fatalf("store failure: %v", err)
	}
	if err := p.Purge(); vaultidentity.StatusOf(err) != vaultidentity.StatusUnavailable {
		t.Fatalf("purge during store failure: %v", err)
	}
}

func TestAbsentOrUnavailableServiceLeavesTheAppPasswordPath(t *testing.T) {
	absent := vaultidentity.NewClient(filepath.Join(t.TempDir(), "none.sock"))
	if c := probeCustody(absent); c.mode != custodyPassword || c.launcherPID != 0 {
		t.Fatalf("absent service custody = %+v", c)
	}
	if _, ok := probeCustody(absent).provider("/cfg/credential-vault.age").(*passphraseIdentityProvider); !ok {
		t.Fatal("absent service did not select the app password")
	}
	_, client := startFakeService(t, vaultidentity.Response{Status: vaultidentity.StatusUnavailable, Code: "manifest_unreadable"})
	if c := probeCustody(client); c.mode != custodyPassword {
		t.Fatalf("unavailable service custody = %+v", c)
	}
}

func TestRefusedEngineCannotOpenTheVaultOnAnyPath(t *testing.T) {
	_, client := startFakeService(t, vaultidentity.Response{Status: vaultidentity.StatusRefused, Code: "executable_mismatch", Message: "pw for pid 7"})
	c := probeCustody(client)
	if c.mode != custodyRefused || c.launcherPID != 0 {
		t.Fatalf("custody = %+v", c)
	}
	p := c.provider("/cfg/credential-vault.age")
	if _, err := p.LoadOrCreate(false); !errors.Is(err, ErrLauncherRefused) || vaultidentity.StatusOf(errors.Unwrap(err)) == vaultidentity.StatusOK {
		t.Fatalf("load: %v", err)
	}
	if err := p.Purge(); !errors.Is(err, ErrLauncherRefused) {
		t.Fatalf("purge: %v", err)
	}
	if p.Describe() == "" {
		t.Fatal("refused provider has no description")
	}
}

func TestCredentialProtectionProbeRefusesIsolatedProvider(t *testing.T) {
	if err := VerifyIdentityProtection(); err == nil {
		t.Fatal("an isolated test process qualified the release credential provider")
	}
}
