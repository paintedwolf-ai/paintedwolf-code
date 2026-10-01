package prompts_test

import (
	"context"
	"sync"
	"testing"
	"testing/synctest"

	"github.com/lycaon/lycaon/internal/prompts"
	"github.com/lycaon/lycaon/internal/testutil"
)

// TestRenderPersonaIsDeterministic protects prompt cache keys.
func TestRenderPersonaIsDeterministic(t *testing.T) {
	prompts.ResetPersonaContractCache()
	engine := prompts.NewFileTemplateEngineLayers(prompts.PromptLayers{})
	ctx := context.Background()

	// Cover distinct persona shapes.
	agents := []string{"implementer", "plan-reviewer", "code-reviewer", "repo-researcher"}
	renders := 25
	if testing.Short() {
		renders = 3
	}

	for _, agent := range agents {
		t.Run(agent, func(t *testing.T) {
			first, err := prompts.RenderPersona(ctx, engine, agent, map[string]any{
				"project_dir": "/tmp/det",
			})
			if err != nil {
				t.Fatalf("first render: %v", err)
			}
			for i := 1; i < renders; i++ {
				got, err := prompts.RenderPersona(ctx, engine, agent, map[string]any{
					"project_dir": "/tmp/det",
				})
				if err != nil {
					t.Fatalf("render %d: %v", i, err)
				}
				if got != first {
					t.Fatalf("render %d differs from first (len %d→%d) — non-deterministic persona render breaks the prompt assembly cache",
						i, len(first), len(got))
				}
			}
		})
	}
}

// TestRenderPersonaConcurrentSafe stresses shared render caches.
func TestRenderPersonaConcurrentSafe(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		testutil.SkipIfShort(t, "concurrent persona render stress")
		prompts.ResetPersonaContractCache()
		engine := prompts.NewFileTemplateEngineLayers(prompts.PromptLayers{})
		ctx := t.Context()

		const (
			goroutines = 16
			renders    = 25
		)
		agent := "implementer"

		want, err := prompts.RenderPersona(ctx, engine, agent, nil)
		testutil.FailErr(t, "prompts.RenderPersona failed", err)

		var (
			wg       sync.WaitGroup
			mu       sync.Mutex
			mismatch string
		)
		wg.Add(goroutines)
		for g := 0; g < goroutines; g++ {
			go func() {
				defer wg.Done()
				for i := 0; i < renders; i++ {
					got, err := prompts.RenderPersona(ctx, engine, agent, nil)
					if err != nil {
						mu.Lock()
						if mismatch == "" {
							mismatch = "err: " + err.Error()
						}
						mu.Unlock()
						return
					}
					if got != want {
						mu.Lock()
						if mismatch == "" {
							mismatch = "output drifted under concurrent render"
						}
						mu.Unlock()
						return
					}
				}
			}()
		}
		wg.Wait()
		if mismatch != "" {
			t.Fatal(mismatch)
		}
	})
}

// TestFileEngineRenderDeterministic covers templates and includes.
func TestFileEngineRenderDeterministic(t *testing.T) {
	engine := prompts.NewFileTemplateEngineLayers(prompts.PromptLayers{})
	ctx := context.Background()

	templates := []string{
		"agents/coordinator-core.md",
		"agents/implementer.md",
	}
	const renders = 50

	for _, ref := range templates {
		t.Run(ref, func(t *testing.T) {
			first, err := engine.Render(ctx, ref, map[string]any{"project_dir": "/tmp/det"})
			if err != nil {
				t.Fatalf("first render: %v", err)
			}
			for i := 1; i < renders; i++ {
				got, err := engine.Render(ctx, ref, map[string]any{"project_dir": "/tmp/det"})
				if err != nil {
					t.Fatalf("render %d: %v", i, err)
				}
				if got != first {
					t.Fatalf("render %d differs (len %d→%d) — non-deterministic template render",
						i, len(first), len(got))
				}
			}
		})
	}
}
