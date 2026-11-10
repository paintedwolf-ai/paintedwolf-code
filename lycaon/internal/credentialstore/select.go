package credentialstore

import (
	"flag"
	"fmt"
	"path/filepath"
	"strings"
	"sync"

	"github.com/lycaon/lycaon/internal/configdir"
)

type vaultCacheKey struct {
	path    string
	custody providerCustodyKey
}

var vaultPathLocks = struct {
	sync.Mutex
	byPath map[string]*sync.Mutex
}{byPath: make(map[string]*sync.Mutex)}

func vaultPathMutex(path string) *sync.Mutex {
	clean := filepath.Clean(path)
	vaultPathLocks.Lock()
	defer vaultPathLocks.Unlock()
	mu := vaultPathLocks.byPath[clean]
	if mu == nil {
		mu = &sync.Mutex{}
		vaultPathLocks.byPath[clean] = mu
	}
	return mu
}

var vaults = struct {
	sync.Mutex
	byKey map[vaultCacheKey]*vaultFile
}{byKey: make(map[vaultCacheKey]*vaultFile)}

func sharedVault(path string, provider identityProvider) *vaultFile {
	clean := filepath.Clean(path)
	if provider == nil {
		return &vaultFile{path: clean}
	}
	custody := provider.custodyKey()
	if !custody.canShare() {
		return &vaultFile{path: clean, provider: provider}
	}
	key := vaultCacheKey{path: clean, custody: custody}
	vaults.Lock()
	defer vaults.Unlock()
	if existing := vaults.byKey[key]; existing != nil {
		return existing
	}
	created := &vaultFile{path: clean, provider: provider}
	vaults.byKey[key] = created
	return created
}

// clearCachedVaultIdentities resets cached identities for path. Callers must
// hold vaultPathMutex(path) via withExclusiveLock.
func clearCachedVaultIdentities(path string) {
	clean := filepath.Clean(path)
	vaults.Lock()
	var matched []*vaultFile
	for key, v := range vaults.byKey {
		if key.path == clean {
			matched = append(matched, v)
		}
	}
	vaults.Unlock()
	for _, v := range matched {
		v.identity, v.vaultID = nil, ""
	}
}

func isolatedProcess() bool {
	return flag.Lookup("test.v") != nil || configdir.IsPerformanceBuild()
}

func selectBackend(slot Slot) (Backend, error) {
	path := strings.TrimSpace(slot.Path)
	namespace := strings.TrimSpace(slot.Namespace)
	if path == "" {
		return nil, fmt.Errorf("credential vault path required for %s", slot.Context)
	}
	if namespace == "" {
		return nil, fmt.Errorf("credential namespace required for %s", slot.Context)
	}
	provider := selectIdentityProvider(path, isolatedProcess(), releaseBuild())
	return newNamespaceBackend(path, namespace, provider), nil
}

func selectVault(path string) (*vaultFile, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return nil, fmt.Errorf("credential vault path required")
	}
	provider := selectIdentityProvider(path, isolatedProcess(), releaseBuild())
	return sharedVault(path, provider), nil
}

func newDevelopmentBackend(path, namespace string) Backend {
	return newNamespaceBackend(path, namespace, newDevelopmentIdentityProvider(path))
}
