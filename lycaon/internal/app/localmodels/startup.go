package localmodels

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/lycaon/lycaon/internal/configdir"
	"github.com/lycaon/lycaon/internal/configlayout"
	"github.com/lycaon/lycaon/internal/decide/bialy"
	"github.com/lycaon/lycaon/internal/hostprocess"
	"github.com/lycaon/lycaon/internal/inference"
	"github.com/lycaon/lycaon/internal/llm/providers/nativegen"
	"github.com/lycaon/lycaon/internal/modelstore"
)

// Startup retains initialization facts without making external providers depend on local hardware.
type Startup struct {
	OfflineInstaller bool
	OfflineTier      string
	Runtime          *Runtime
	Store            *modelstore.Store
	Decision         bialy.Config
	Issue            error
}

// Start resolves the co-versioned executable, shaders, release keys, and seed.
func Start(ctx context.Context, fixture bool) Startup {
	result := Startup{Decision: bialy.ConfigFromEnvironment()}
	if fixture {
		return result
	}
	binary := configlayout.SiblingExecutable("nativegen")
	root := configlayout.EngineRoot()
	if root != "" {
		marker, err := os.ReadFile(filepath.Join(root, "generation", "offline-model"))
		result.OfflineInstaller = !errors.Is(err, os.ErrNotExist)
		if err != nil && result.OfflineInstaller {
			result.Issue = err
			return result
		}
		result.OfflineTier = strings.TrimSpace(string(marker))
	}

	config, err := configdir.UserConfigDir()
	if err != nil {
		result.Issue = err
		return result
	}
	backend := "vulkan"
	metal := ""
	if runtime.GOOS == "darwin" {
		backend = "mlx"
		metal = filepath.Join(root, "generation", "mlx.metallib")
	}
	authorityPath := filepath.Join(root, "generation", "release-keys.json")
	var authority *modelstore.Authority
	if root != "" {
		authority, err = readAuthority(authorityPath)
	} else {
		authority, err = modelstore.NewAuthority(nil)
	}
	if err != nil {
		result.Issue = err
		return result
	}
	result.Store = modelstore.New(filepath.Join(config, "local-models"), authority)
	if binary == "" || root == "" {
		result.Issue = errors.New("local generation adapter is unavailable")
		return result
	}
	result.Runtime, result.Issue = Build(ctx, Options{Store: result.Store,
		Binary: binary, MetalLibrary: metal,
		Authority:   authorityPath,
		Seed:        filepath.Join(root, "generation", "models", backend),
		StoreRoot:   filepath.Join(config, "local-models"),
		PhysicalRAM: hostprocess.PhysicalMemoryBytes(), Decision: result.Decision,
	})
	if result.Runtime != nil {
		result.Decision = result.Runtime.Decision
	}
	return result
}

func (s *Startup) Generation() *nativegen.Availability {
	if s.Runtime != nil {
		return &nativegen.Availability{Runtime: s.Runtime.Generation, OfflineInstaller: s.OfflineInstaller, OfflineTier: s.OfflineTier}
	}
	if s.Issue == nil {
		return nil
	}
	status := nativegen.Readiness{State: "unavailable", Action: "none", Detail: s.Issue.Error()}
	if errors.Is(s.Issue, inference.ErrCapacity) {
		status.State = "insufficient_memory"
	}
	return &nativegen.Availability{Initial: status, Store: s.Store, OfflineInstaller: s.OfflineInstaller, OfflineTier: s.OfflineTier}
}

func (s *Startup) CloseGeneration(context.Context) error {
	if s.Runtime == nil {
		return nil
	}
	return s.Runtime.Generation.Close()
}

func (s *Startup) CloseResources(context.Context) error {
	if s.Runtime != nil {
		s.Runtime.Resources.Close()
	}
	return nil
}
