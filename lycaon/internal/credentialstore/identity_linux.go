package credentialstore

import (
	"errors"
	"fmt"
	"os"

	"github.com/lycaon/lycaon/internal/vaultidentity"
)

type custodyMode int

const (
	// The zero value cannot establish custody.
	custodyUnavailable custodyMode = iota
	custodyService
	custodyRefused
)

// custody is the outcome of asking the identity service who this engine is.
type custody struct {
	mode        custodyMode
	launcherPID int
	client      *vaultidentity.Client
	refusal     error
	// absent marks an unavailable custodian that is not installed, not
	// running, or cannot serve, as opposed to one briefly unreachable.
	absent bool
}

// serviceExecutable is the packaged identity service; a test points it at a
// fixture.
var serviceExecutable = "/opt/painted-wolf-code/bin/pw-vault-identity"

// serviceInstalled reports the structured fact that the identity service
// program is installed.
func serviceInstalled() bool {
	info, err := os.Stat(serviceExecutable)
	return err == nil && info.Mode().IsRegular()
}

func newServiceClient() *vaultidentity.Client {
	return vaultidentity.NewClient(vaultidentity.SocketPath)
}

// probeCustody preserves an unavailable custodian separately from a refusal.
func probeCustody(client *vaultidentity.Client) custody {
	resp, err := client.Do(vaultidentity.Request{Op: vaultidentity.OpProbe})
	if err != nil {
		_ = client.Close()
		// No socket or nobody listening on it means the service is not
		// enabled or not installed; a failure on a live socket is transient.
		absent := errors.Is(err, vaultidentity.ErrServiceAbsent) || !serviceInstalled()
		return custody{mode: custodyUnavailable, refusal: err, absent: absent}
	}
	switch resp.Status {
	case vaultidentity.StatusOK:
		return custody{mode: custodyService, launcherPID: resp.LauncherPID, client: client}
	case vaultidentity.StatusRefused:
		_ = client.Close()
		return custody{mode: custodyRefused, refusal: resp.Err()}
	default:
		// The service answered that it cannot serve this installation.
		_ = client.Close()
		return custody{mode: custodyUnavailable, refusal: resp.Err(), absent: true}
	}
}

func (c custody) provider(path string) identityProvider {
	switch c.mode {
	case custodyService:
		return &itemsIdentityProvider{account: identityAccount(path),
			description: "identity held by the Painted Wolf Code vault identity service",
			items:       serviceItems{client: c.client}}
	case custodyRefused:
		return refusedIdentityProvider{refusal: c.refusal}
	default:
		code := ErrCustodianUnavailable
		if c.absent {
			code = ErrCustodianAbsent
		}
		return unavailableIdentityProvider{refusal: fmt.Errorf("%w; restore the Painted Wolf Code vault identity service and reopen the installed app", errors.Join(code, c.refusal))}
	}
}

// serviceItems is the identity service as a custodian.
type serviceItems struct {
	client *vaultidentity.Client
}

func (s serviceItems) exchange(req vaultidentity.Request) (vaultidentity.Response, error) {
	resp, err := s.client.Do(req)
	if err != nil {
		return vaultidentity.Response{}, fmt.Errorf("%w: %w", ErrCustodianUnavailable, err)
	}
	switch resp.Status {
	case vaultidentity.StatusOK:
		return resp, nil
	case vaultidentity.StatusNotFound:
		return resp, errIdentityMissing
	case vaultidentity.StatusExists:
		return resp, errIdentityExists
	case vaultidentity.StatusRefused:
		return resp, errors.Join(ErrLauncherRefused, resp.Err())
	default:
		return resp, errors.Join(ErrCustodianUnavailable, resp.Err())
	}
}

func (s serviceItems) read(account string) ([]byte, error) {
	resp, err := s.exchange(vaultidentity.Request{Op: vaultidentity.OpRead, Account: account})
	if err != nil {
		return nil, err
	}
	if len(resp.Value) == 0 {
		return nil, fmt.Errorf("%w: the service returned an empty identity", ErrVaultCorrupt)
	}
	return resp.Value, nil
}

func (s serviceItems) create(account string, value []byte) error {
	_, err := s.exchange(vaultidentity.Request{Op: vaultidentity.OpCreate, Account: account, Value: value})
	return err
}

func (s serviceItems) remove(account string) error {
	_, err := s.exchange(vaultidentity.Request{Op: vaultidentity.OpRemove, Account: account})
	return err
}

// refusedIdentityProvider fails every operation with guidance: the service
// holds the identity and must verify the installed launcher.
type refusedIdentityProvider struct {
	refusal error
}

func (refusedIdentityProvider) Describe() string {
	return "identity held by the Painted Wolf Code vault identity service (this engine was refused)"
}

func (p refusedIdentityProvider) LoadOrCreate(bool) (identityDocument, error) {
	return identityDocument{}, fmt.Errorf("%w: %w; start Painted Wolf Code from the installed desktop app, "+
		"then restore access to the vault identity service", ErrLauncherRefused, p.refusal)
}

func (p refusedIdentityProvider) Purge() error {
	return fmt.Errorf("%w: %w", ErrLauncherRefused, p.refusal)
}
