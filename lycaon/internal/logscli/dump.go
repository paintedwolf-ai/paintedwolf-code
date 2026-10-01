package logscli

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/lycaon/lycaon/internal/configlayout"
	"github.com/lycaon/lycaon/internal/coordinator/anchor"
	"github.com/lycaon/lycaon/internal/coordinator/assembly"
	"github.com/lycaon/lycaon/internal/prompts"
)

// runDump renders a synthetic coordinator prompt.
func runDump(out *bufio.Writer, f flags) error {
	fixture := strings.TrimSpace(f.fixture)
	if fixture == "" {
		fixture = "implement_investigate_idle"
	}

	configRoot := configlayout.FindModuleRoot()
	if root := strings.TrimSpace(os.Getenv("LYCAON_CONFIG_ROOT")); root != "" {
		configRoot = filepath.Join(root, "lycaon")
	}
	engine := prompts.NewFileTemplateEngineLayers(prompts.PromptLayers{ModuleRoot: configRoot})
	// Transition injects compile with the effective anchor registry.
	if anchor.DefaultRegistry() == nil {
		reg, err := anchor.LoadRegistryFromConfigRoot()
		if err != nil {
			return fmt.Errorf("load anchor registry: %w", err)
		}
		anchor.SetDefaultRegistry(reg)
	}

	rendered, err := assembly.RenderPromptDump(
		context.Background(),
		engine,
		configRoot,
		assembly.PromptDumpOptions{
			FixtureName:    fixture,
			SessionID:      f.session,
			PreviousFamily: f.previous,
			VerifyRequired: f.verify,
		},
	)
	if err != nil {
		return err
	}
	fmt.Fprint(out, rendered)
	return nil
}
