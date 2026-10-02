package credentialstore

import (
	"path/filepath"
	"strings"

	"github.com/lycaon/lycaon/internal/configdir"
)

const (
	// VaultBasename names the encrypted credential document.
	VaultBasename = "credential-vault.age"
	// IdentityBasename holds the age identity encrypted by the app password.
	IdentityBasename = "credential-vault-identity.age"
	// DevelopmentIdentityBasename names the development identity file.
	DevelopmentIdentityBasename = ".credential-vault-development-identity"
)

// UnlockStdinEnv tells the engine to read one framed app password from stdin.
const UnlockStdinEnv = "LYCAON_CREDENTIAL_UNLOCK_STDIN"

// Vault namespaces are durable credential identifiers.
const (
	NamespaceProviders         = "providers"
	NamespaceWebResearch       = "web-research"
	NamespaceMCPOAuth          = "mcp-oauth"
	NamespaceSecretFingerprint = "secret-fingerprint"
	NamespaceManagedSecrets    = "managed-secrets"
	NamespacePresenceReleases  = "presence-releases"
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

func identityPath(vaultPath string) string {
	return filepath.Join(filepath.Dir(vaultPath), IdentityBasename)
}

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
	return unattendedIdentityProvider && releaseBuild() && !isolatedProcess()
}
