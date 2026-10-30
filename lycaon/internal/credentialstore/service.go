package credentialstore

import (
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
