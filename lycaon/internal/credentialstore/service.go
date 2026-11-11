package credentialstore

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/lycaon/lycaon/internal/configdir"
)

const (
	// VaultBasename names the encrypted credential document.
	VaultBasename = "credential-vault.age"
	// DevelopmentIdentityBasename names the development identity file.
	DevelopmentIdentityBasename = ".credential-vault-development-identity"
)

// Vault namespaces are durable credential identifiers.
const (
	NamespaceProviders         = "providers"
	NamespaceWebResearch       = "web-research"
	NamespaceMCPOAuth          = "mcp-oauth"
	NamespaceSecretFingerprint = "secret-fingerprint"
	NamespaceManagedSecrets    = "managed-secrets"
	NamespaceGitCredentials    = "git-credentials"
	NamespaceCredentialFiles   = "credential-file-evidence"
	NamespaceHostIdentity      = "host-identity"
)

// Slot declares one validated namespace inside the shared encrypted vault.
type Slot struct {
	// Path is the shared encrypted vault path.
	Path string
	// Namespace is the durable segment inside the vault.
	Namespace string
	// Context labels the namespace in error messages.
	Context string
}

// DefaultVaultPath returns the credential vault path.
func DefaultVaultPath(dir string) string { return filepath.Join(dir, VaultBasename) }

func developmentIdentityPath(vaultPath string) string {
	if filepath.Base(vaultPath) == VaultBasename {
		return filepath.Join(filepath.Dir(vaultPath), DevelopmentIdentityBasename)
	}
	name := strings.TrimSuffix(filepath.Base(vaultPath), filepath.Ext(vaultPath))
	return filepath.Join(filepath.Dir(vaultPath), "."+name+"-development-identity")
}

func releaseBuild() bool { return !configdir.IsDevelopmentBuild() }

// UnlocksUnattended reports whether the vault opens without a human secret:
// any process that can start the signed engine reaches its plaintext.
func UnlocksUnattended() bool {
	return releaseBuild() && !isolatedProcess() && unattendedIdentity()
}

// VerifiedLauncherPID is the desktop app a custodian outside this process
// verified as the engine's parent while releasing the identity, or 0 where
// no custodian verifies launchers.
func VerifiedLauncherPID() int {
	if !releaseBuild() || isolatedProcess() {
		return 0
	}
	return verifiedLauncherPID()
}

func (v *vaultFile) verifyDurableLocked(provider identityProvider, namespace, id, want string) error {
	fresh := &vaultFile{path: v.path, provider: provider}
	doc, err := fresh.read()
	if err != nil {
		return err
	}
	values := doc.Namespaces[namespace]
	if values == nil || values[id] != want {
		return fmt.Errorf("durable credential verification failed for %s/%s", namespace, id)
	}
	return nil
}

func (v *vaultFile) verifyDurable(provider identityProvider, namespace, id, want string) error {
	return v.withExclusiveLock(func() error {
		return v.verifyDurableLocked(provider, namespace, id, want)
	})
}

func (v *vaultFile) loadOrInit(
	provider identityProvider,
	namespace, id string,
	validate func(string) error,
	init func() (string, error),
	afterVerified func() error,
) (string, error) {
	var result string
	err := v.withExclusiveLock(func() error {
		_, statErr := os.Stat(v.path)
		if statErr != nil && !os.IsNotExist(statErr) {
			return fmt.Errorf("read credential vault: %w", statErr)
		}
		vaultExists := statErr == nil
		var doc vaultDocument
		if vaultExists {
			var err error
			doc, err = v.read()
			if err != nil {
				return err
			}
			if values := doc.Namespaces[namespace]; values != nil {
				if existing := values[id]; existing != "" {
					if validate != nil {
						if err := validate(existing); err != nil {
							return err
						}
					}
					if err := v.verifyDurableLocked(provider, namespace, id, existing); err != nil {
						return err
					}
					if afterVerified != nil {
						if err := afterVerified(); err != nil {
							return err
						}
					}
					result = existing
					return nil
				}
			}
		}
		secret, err := init()
		if err != nil {
			return err
		}
		if validate != nil {
			if err := validate(secret); err != nil {
				return err
			}
		}
		if !vaultExists {
			doc, err = v.read()
			if err != nil {
				return err
			}
		}
		values := doc.Namespaces[namespace]
		if values == nil {
			values = make(map[string]string)
			doc.Namespaces[namespace] = values
		}
		values[id] = secret
		if err := v.commit(doc); err != nil {
			return err
		}
		if err := v.verifyDurableLocked(provider, namespace, id, secret); err != nil {
			delete(values, id)
			if len(values) == 0 {
				delete(doc.Namespaces, namespace)
			}
			var restoreErr error
			if vaultExists {
				restoreErr = v.commit(doc)
			} else {
				if rmErr := os.Remove(v.path); rmErr != nil && !os.IsNotExist(rmErr) {
					restoreErr = rmErr
				}
			}
			if restoreErr != nil {
				return errors.Join(err, fmt.Errorf("restore credential vault after verification failure: %w", restoreErr))
			}
			return err
		}
		if afterVerified != nil {
			if err := afterVerified(); err != nil {
				return err
			}
		}
		result = secret
		return nil
	})
	return result, err
}
