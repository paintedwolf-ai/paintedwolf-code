package credentialstore

import (
	"flag"
	"fmt"
	"strings"

	"github.com/lycaon/lycaon/internal/configdir"
)

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
