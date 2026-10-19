package credentialstore

import (
	"errors"
	"fmt"

	"github.com/lycaon/lycaon/internal/vaultidentity"
)

type custodyMode int

const (
	// custodyPassword: password custody was explicitly selected.
	custodyPassword custodyMode = iota
	// custodyService: the service verified this engine and holds the identity.
	custodyService
	// custodyRefused: the service runs but refused this engine.
	custodyRefused
	// custodyUnavailable: service custody could not be established.
	custodyUnavailable
)

// custody is the outcome of asking the identity service who this engine is.
type custody struct {
	mode        custodyMode
	launcherPID int
	client      *vaultidentity.Client
	refusal     error
}

func newServiceClient() *vaultidentity.Client {
	return vaultidentity.NewClient(vaultidentity.SocketPath)
}

// probeCustody preserves an unavailable custodian separately from a refusal.
// Password operation requires an existing wrapper or a new vault.
func probeCustody(client *vaultidentity.Client) custody {
	resp, err := client.Do(vaultidentity.Request{Op: vaultidentity.OpProbe})
	if err != nil {
		_ = client.Close()
		return custody{mode: custodyUnavailable, refusal: err}
	}
	switch resp.Status {
	case vaultidentity.StatusOK:
		return custody{mode: custodyService, launcherPID: resp.LauncherPID, client: client}
	case vaultidentity.StatusRefused:
		_ = client.Close()
		return custody{mode: custodyRefused, refusal: resp.Err()}
	default:
		_ = client.Close()
		return custody{mode: custodyUnavailable, refusal: resp.Err()}
	}
}

func (c custody) provider(path string) identityProvider {
	switch c.mode {
	case custodyService:
		return c.serviceHost(path)
	case custodyRefused:
		return refusedIdentityProvider{refusal: c.refusal}
	case custodyUnavailable:
		return &passphraseIdentityProvider{
			path: identityPath(path), missingError: ErrCustodianUnavailable,
			missingGuidance: "; restore the vault identity service and open the installed desktop app; " +
				"an app password requires a previously exported identity",
		}
	default:
		return newPassphraseIdentityProvider(path)
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
// holds the identity, so no password could open the vault either.
type refusedIdentityProvider struct {
	refusal error
}

func (refusedIdentityProvider) Describe() string {
	return "identity held by the Painted Wolf Code vault identity service (this engine was refused)"
}

func (p refusedIdentityProvider) LoadOrCreate(bool) (identityDocument, error) {
	return identityDocument{}, fmt.Errorf("%w: %w; start Painted Wolf Code from the installed desktop app, "+
		"then restore access to the vault identity service; moving to an app password requires a verified export first", ErrLauncherRefused, p.refusal)
}

func (p refusedIdentityProvider) Purge() error {
	return fmt.Errorf("%w: %w", ErrLauncherRefused, p.refusal)
}
