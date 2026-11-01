package localmodels

import (
	"context"
	"path/filepath"
	"runtime"

	"github.com/lycaon/lycaon/internal/configdir"
	"github.com/lycaon/lycaon/internal/configlayout"
	"github.com/lycaon/lycaon/internal/decide/bialy"
	"github.com/lycaon/lycaon/internal/hostprocess"
	"github.com/lycaon/lycaon/internal/llm/providers/nativegen"
)

// Startup retains initialization facts without making external providers depend on local hardware.
type Startup struct {
	Runtime  *Runtime
	Decision bialy.Config
	Issue    error
}

// Start resolves the co-versioned executable, shaders, release keys, and seed.
func Start(ctx context.Context, fixture bool) Startup {
	result := Startup{Decision: bialy.ConfigFromEnvironment()}
	if fixture {
		return result
	}
	binary := configlayout.SiblingExecutable("nativegen")
	root := configlayout.EngineRoot()
	if binary == "" || root == "" {
		return result
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
	result.Runtime, result.Issue = Build(ctx, Options{
		Binary: binary, MetalLibrary: metal,
		Authority: filepath.Join(root, "generation", "release-keys.json"),
		Seed: filepath.Join(root, "generation", "models", backend),
		StoreRoot: filepath.Join(config, "local-models"),
		PhysicalRAM: hostprocess.PhysicalMemoryBytes(), Decision: result.Decision,
	})
	if result.Runtime != nil {
		result.Decision = result.Runtime.Decision
	}
	return result
}

func (s *Startup) Generation() *nativegen.Runtime {
	if s.Runtime == nil {
		return nil
	}
	return s.Runtime.Generation
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
