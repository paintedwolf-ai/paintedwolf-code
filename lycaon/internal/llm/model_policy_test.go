package llm

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/config"
	"github.com/lycaon/lycaon/config/configtest"
	"github.com/lycaon/lycaon/internal/settingsoverlay"
	"github.com/lycaon/lycaon/internal/testutil"
)

const emptyModelPolicyYAML = `coordinator:
  provider_id: ""
  model: ""
lite:
  provider_id: ""
  model: ""
agent_pool:
  selection: first
  models: []
`

func mustPolicy(t *testing.T, store *PolicyStore, scope SettingsScope, projectDir string) ModelPolicy {
	t.Helper()
	p, err := store.Get(scope, projectDir)
	testutil.FailErr(t, "get model policy", err)
	return p
}

func mustPolicyOverlay(t *testing.T, store *PolicyStore, scope SettingsScope, projectDir string) ModelPolicy {
	t.Helper()
	p, err := store.Overlay(scope, projectDir)
	testutil.FailErr(t, "get model policy overlay", err)
	return p
}

func TestPolicyStoreGetForProjectRootsUsesPrimaryThenActive(t *testing.T) {
	configtest.Overlay(t, map[config.Rel]string{config.ModelPolicy: emptyModelPolicyYAML})
	store, err := NewPolicyStoreAt(filepath.Join(t.TempDir(), "model-policy.yaml"))
	testutil.FailErr(t, "create policy store", err)
	primary := t.TempDir()
	active := t.TempDir()
	testutil.FailErr(t, "set primary policy", store.PutProject(primary, ModelPolicy{
		Coordinator: ModelRef{ProviderID: "primary", Model: "coordinator"},
		Lite:        ModelRef{ProviderID: "primary", Model: "lite"},
	}))
	testutil.FailErr(t, "set active policy", store.PutProject(active, ModelPolicy{
		Coordinator: ModelRef{ProviderID: "active", Model: "coordinator"},
	}))

	got, err := store.GetForProjectRoots([]string{primary, active})
	testutil.FailErr(t, "resolve project roots", err)
	if got.Coordinator.ProviderID != "active" || got.Lite.ProviderID != "primary" {
		t.Fatalf("policy = %+v; want active coordinator and primary lite", got)
	}
}

func TestNewPolicyStoreRejectsMalformedGlobalPolicy(t *testing.T) {
	configDir := t.TempDir()
	t.Setenv("LYCAON_CONFIG_DIR", configDir)
	path := filepath.Join(configDir, settingsoverlay.BasenameModelPolicy)
	testutil.FailErr(t, "write malformed policy", os.WriteFile(path, []byte("coordinator: ["), 0o600))

	_, err := NewPolicyStore()
	if err == nil || !strings.Contains(err.Error(), "parse model policy") {
		t.Fatalf("NewPolicyStore error = %v, want malformed global policy error", err)
	}
}

func TestNewPolicyStoreRejectsUnknownGlobalPolicyFields(t *testing.T) {
	configDir := t.TempDir()
	t.Setenv("LYCAON_CONFIG_DIR", configDir)
	path := filepath.Join(configDir, settingsoverlay.BasenameModelPolicy)
	testutil.FailErr(t, "write invalid policy", os.WriteFile(path, []byte("coordinatr: {}\n"), 0o600))

	if _, err := NewPolicyStore(); err == nil {
		t.Fatal("unknown policy field was accepted")
	}
}

func TestNewPolicyStoreRejectsPartialModelReference(t *testing.T) {
	configDir := t.TempDir()
	t.Setenv("LYCAON_CONFIG_DIR", configDir)
	path := filepath.Join(configDir, settingsoverlay.BasenameModelPolicy)
	testutil.FailErr(t, "write invalid policy", os.WriteFile(path, []byte("coordinator:\n  provider_id: openai\n"), 0o600))

	if _, err := NewPolicyStore(); err == nil {
		t.Fatal("partial model reference was accepted")
	}
}

func TestPolicyWritesRejectInvalidSelection(t *testing.T) {
	configtest.Overlay(t, map[config.Rel]string{config.ModelPolicy: emptyModelPolicyYAML})
	store, err := NewPolicyStoreAt(filepath.Join(t.TempDir(), "model-policy.yaml"))
	testutil.FailErr(t, "create policy store", err)
	invalid := ModelPolicy{AgentPool: AgentPool{Selection: "newest"}}

	if err := store.PutGlobal(invalid); err == nil {
		t.Fatal("global write accepted invalid selection")
	}
	if err := store.PutProject(t.TempDir(), invalid); err == nil {
		t.Fatal("project write accepted invalid selection")
	}
}

func TestServiceClassifiesInvalidPolicyShape(t *testing.T) {
	sel := PoolSelection("newest")
	err := (&Service{}).ApplyModelPolicy(t.Context(), SettingsScopeGlobal, "", ModelPolicyPatch{
		AgentPool: &AgentPool{Selection: sel},
	})
	var validation *PolicyValidationError
	if !errors.As(err, &validation) {
		t.Fatalf("error = %v, want PolicyValidationError", err)
	}
}

func TestSummarizerRefUsesLiteOverride(t *testing.T) {
	p := ModelPolicy{
		Coordinator: ModelRef{ProviderID: "openai", Model: "gpt-4o"},
		Lite:        ModelRef{ProviderID: "ollama", Model: "llama3.1"},
	}
	got := SummarizerRef(p)
	if got != p.Lite {
		t.Fatalf("SummarizerRef() = %+v want lite %+v", got, p.Lite)
	}
}

func TestSummarizerRefFallsBackToCoordinator(t *testing.T) {
	p := ModelPolicy{
		Coordinator: ModelRef{ProviderID: "openai", Model: "gpt-4o"},
	}
	got := SummarizerRef(p)
	if got != p.Coordinator {
		t.Fatalf("SummarizerRef() = %+v want coordinator %+v", got, p.Coordinator)
	}
}

func TestSummarizerSharesCoordinator(t *testing.T) {
	shared := ModelPolicy{
		Coordinator: ModelRef{ProviderID: "openai", Model: "gpt-4o"},
		Lite:        ModelRef{ProviderID: "openai", Model: "gpt-4o-mini"},
	}
	if !SummarizerSharesCoordinator(shared) {
		t.Fatal("want shared when lite uses coordinator provider")
	}
	dedicated := ModelPolicy{
		Coordinator: ModelRef{ProviderID: "openai", Model: "gpt-4o"},
		Lite:        ModelRef{ProviderID: "ollama", Model: "llama3.1"},
	}
	if SummarizerSharesCoordinator(dedicated) {
		t.Fatal("want not shared when lite uses another provider")
	}
	if !SummarizerSharesCoordinator(ModelPolicy{
		Coordinator: ModelRef{ProviderID: "openai", Model: "gpt-4o"},
	}) {
		t.Fatal("unset lite inherits coordinator provider")
	}
}

func TestMergePolicyEmptyInheritsBase(t *testing.T) {
	base := ModelPolicy{
		Coordinator: ModelRef{ProviderID: "openai", Model: "gpt-4o-mini"},
		Lite:        ModelRef{ProviderID: "openai", Model: "gpt-4o-mini"},
		AgentPool: AgentPool{
			Selection: PoolSelectionRoundRobin,
			Models: []ModelRef{
				{ProviderID: "openai", Model: "gpt-4o-mini"},
			},
		},
	}
	inherited := mergePolicy(base, ModelPolicy{
		AgentPool: AgentPool{Selection: PoolSelectionFirst},
	})
	if inherited.Coordinator != base.Coordinator || inherited.Lite != base.Lite {
		t.Fatalf("empty overlay must inherit refs: %+v", inherited)
	}
	if len(inherited.AgentPool.Models) != 1 || inherited.AgentPool.Models[0] != base.AgentPool.Models[0] {
		t.Fatalf("empty pool must inherit models: %+v", inherited.AgentPool.Models)
	}
	if inherited.AgentPool.Selection != PoolSelectionFirst {
		t.Fatalf("selection = %q want first", inherited.AgentPool.Selection)
	}
}

func TestMergePolicySetSlotOverridesBase(t *testing.T) {
	base := ModelPolicy{
		Coordinator: ModelRef{ProviderID: "openai", Model: "gpt-4o-mini"},
		Lite:        ModelRef{ProviderID: "openai", Model: "gpt-4o-mini"},
	}
	got := mergePolicy(base, ModelPolicy{
		Coordinator: ModelRef{ProviderID: "anthropic", Model: "claude"},
	})
	if got.Coordinator.ProviderID != "anthropic" || got.Coordinator.Model != "claude" {
		t.Fatalf("coordinator = %+v", got.Coordinator)
	}
	if got.Lite != base.Lite {
		t.Fatalf("unset lite must inherit: %+v", got.Lite)
	}
}

func TestPutGlobalEmptyLiteSurvivesGet(t *testing.T) {
	tmp := t.TempDir()
	global := filepath.Join(tmp, "global.yaml")
	// An unset lite model inherits the coordinator.
	const bundledYAML = `coordinator:
  provider_id: ""
  model: ""
lite:
  provider_id: ""
  model: ""
agent_pool:
  selection: first
  models: []
`
	configtest.Overlay(t, map[config.Rel]string{config.ModelPolicy: bundledYAML})
	store, err := NewPolicyStoreAt(global)
	if err != nil {
		t.Fatalf("NewPolicyStoreAt: %v", err)
	}
	want := ModelPolicy{
		Coordinator: ModelRef{ProviderID: "openai", Model: "gpt-4o"},
		Lite:        ModelRef{},
		AgentPool: AgentPool{
			Selection: PoolSelectionFirst,
			Models:    []ModelRef{{ProviderID: "openai", Model: "gpt-4o"}},
		},
	}
	if err := store.PutGlobal(want); err != nil {
		t.Fatalf("PutGlobal: %v", err)
	}
	got := mustPolicy(t, store, SettingsScopeGlobal, "")
	if got.Lite.ProviderID != "" || got.Lite.Model != "" {
		t.Fatalf("lite after Put/Get = %+v want unset", got.Lite)
	}
	if got.Coordinator != want.Coordinator {
		t.Fatalf("coordinator = %+v want %+v", got.Coordinator, want.Coordinator)
	}
	if SummarizerRef(got) != want.Coordinator {
		t.Fatalf("SummarizerRef = %+v want coordinator", SummarizerRef(got))
	}
}

func TestPolicyPatchLeavesOmittedLite(t *testing.T) {
	tmp := t.TempDir()
	global := filepath.Join(tmp, "global.yaml")
	configtest.Overlay(t, map[config.Rel]string{config.ModelPolicy: emptyModelPolicyYAML})
	store, err := NewPolicyStoreAt(global)
	testutil.FailErr(t, "create policy store", err)
	testutil.FailErr(t, "seed policy", store.PutGlobal(ModelPolicy{
		Coordinator: ModelRef{ProviderID: "fireworks-1", Model: "glm"},
		Lite:        ModelRef{ProviderID: "ollama-1", Model: "gemma4:e2b"},
		AgentPool: AgentPool{
			Selection: PoolSelectionFirst,
			Models:    []ModelRef{{ProviderID: "fireworks-1", Model: "glm"}},
		},
	}))

	coord := ModelRef{ProviderID: "fireworks-1", Model: "kimi"}
	pool := AgentPool{
		Selection: PoolSelectionFirst,
		Models:    []ModelRef{coord},
	}
	current, err := store.Overlay(SettingsScopeGlobal, "")
	testutil.FailErr(t, "read global overlay", err)
	testutil.FailErr(t, "apply coordinator patch", store.PutGlobal(applyPolicyPatch(current, ModelPolicyPatch{
		Coordinator: &coord,
		AgentPool:   &pool,
	})))

	got := mustPolicy(t, store, SettingsScopeGlobal, "")
	if got.Coordinator != coord {
		t.Fatalf("coordinator = %+v want %+v", got.Coordinator, coord)
	}
	if got.Lite.ProviderID != "ollama-1" || got.Lite.Model != "gemma4:e2b" {
		t.Fatalf("lite = %+v, omitted slot must stay stored", got.Lite)
	}
}

func TestProjectOverlayInheritsAndTracksGlobal(t *testing.T) {
	tmp := t.TempDir()
	global := filepath.Join(tmp, "global.yaml")
	projectDir := filepath.Join(tmp, "proj")
	if err := os.MkdirAll(filepath.Join(projectDir, settingsoverlay.DirName()), 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	configtest.Overlay(t, map[config.Rel]string{config.ModelPolicy: emptyModelPolicyYAML})
	store, err := NewPolicyStoreAt(global)
	if err != nil {
		t.Fatalf("NewPolicyStoreAt: %v", err)
	}
	v1 := ModelPolicy{
		Coordinator: ModelRef{ProviderID: "openai", Model: "gpt-4o"},
		AgentPool: AgentPool{
			Selection: PoolSelectionFirst,
			Models:    []ModelRef{{ProviderID: "openai", Model: "gpt-4o"}},
		},
	}
	if err := store.PutGlobal(v1); err != nil {
		t.Fatalf("PutGlobal v1: %v", err)
	}
	overlay := mustPolicyOverlay(t, store, SettingsScopeProject, projectDir)
	if policyHasContent(overlay) {
		t.Fatalf("new project overlay must be empty ---: %+v", overlay)
	}
	effective := mustPolicy(t, store, SettingsScopeProject, projectDir)
	if effective.Coordinator != v1.Coordinator {
		t.Fatalf("effective before override = %+v want global", effective.Coordinator)
	}

	v2 := v1
	v2.Coordinator = ModelRef{ProviderID: "openai", Model: "gpt-4o-mini"}
	v2.AgentPool.Models = []ModelRef{v2.Coordinator}
	if err := store.PutGlobal(v2); err != nil {
		t.Fatalf("PutGlobal v2: %v", err)
	}
	tracked := mustPolicy(t, store, SettingsScopeProject, projectDir)
	if tracked.Coordinator != v2.Coordinator {
		t.Fatalf("empty project must track global change: %+v", tracked.Coordinator)
	}

	override := ModelPolicy{
		Coordinator: ModelRef{ProviderID: "anthropic", Model: "claude"},
	}
	if err := store.PutProject(projectDir, override); err != nil {
		t.Fatalf("PutProject: %v", err)
	}
	gotOverlay := mustPolicyOverlay(t, store, SettingsScopeProject, projectDir)
	if gotOverlay.Coordinator != override.Coordinator {
		t.Fatalf("overlay coordinator = %+v", gotOverlay.Coordinator)
	}
	if modelRefSet(gotOverlay.Lite) {
		t.Fatalf("unset overlay slots must stay empty: %+v", gotOverlay)
	}
	gotEffective := mustPolicy(t, store, SettingsScopeProject, projectDir)
	if gotEffective.Coordinator != override.Coordinator {
		t.Fatalf("effective coordinator = %+v", gotEffective.Coordinator)
	}
	if gotEffective.Lite != v2.Lite {
		t.Fatalf("empty project lite must still track global: %+v", gotEffective.Lite)
	}

	if err := store.PutProject(projectDir, ModelPolicy{}); err != nil {
		t.Fatalf("PutProject clear: %v", err)
	}
	if _, err := os.Stat(projectModelPolicyPath(projectDir)); !os.IsNotExist(err) {
		t.Fatalf("empty project overlay must delete file: err=%v", err)
	}
	if policyHasContent(mustPolicyOverlay(t, store, SettingsScopeProject, projectDir)) {
		t.Fatal("cleared overlay must be empty")
	}
	if mustPolicy(t, store, SettingsScopeProject, projectDir).Coordinator != v2.Coordinator {
		t.Fatal("after clear, effective must track global again")
	}
}

func TestPolicyStoreCanonicalizesProjectCacheKeys(t *testing.T) {
	realProject := t.TempDir()
	aliasRoot := t.TempDir()
	aliasProject := filepath.Join(aliasRoot, "project-link")
	if err := os.Symlink(realProject, aliasProject); err != nil {
		t.Skipf("project path aliases unavailable: %v", err)
	}
	configtest.Overlay(t, map[config.Rel]string{config.ModelPolicy: emptyModelPolicyYAML})
	store, err := NewPolicyStoreAt(filepath.Join(t.TempDir(), "model-policy.yaml"))
	testutil.FailErr(t, "create policy store", err)
	global := ModelPolicy{Coordinator: ModelRef{ProviderID: "global", Model: "default"}}
	testutil.FailErr(t, "put global policy", store.PutGlobal(global))
	override := ModelPolicy{Coordinator: ModelRef{ProviderID: "project", Model: "override"}}
	testutil.FailErr(t, "put project policy through alias", store.PutProject(aliasProject, override))

	if got := mustPolicy(t, store, SettingsScopeProject, realProject).Coordinator; got != override.Coordinator {
		t.Fatalf("canonical project effective coordinator = %+v want %+v", got, override.Coordinator)
	}
	testutil.FailErr(t, "clear project policy through canonical path", store.PutProject(realProject, ModelPolicy{}))
	if got := mustPolicy(t, store, SettingsScopeProject, aliasProject).Coordinator; got != global.Coordinator {
		t.Fatalf("alias effective coordinator after clear = %+v want %+v", got, global.Coordinator)
	}
}

func TestProjectOverlayLoadedFromDiskIsIndexedForProviderDeletion(t *testing.T) {
	tmp := t.TempDir()
	projectDir := filepath.Join(tmp, "proj")
	overlayDir := filepath.Join(projectDir, settingsoverlay.DirName())
	testutil.FailErr(t, "mkdir project overlay", os.MkdirAll(overlayDir, 0o700))
	testutil.FailErr(t, "write project policy", os.WriteFile(
		filepath.Join(overlayDir, settingsoverlay.BasenameModelPolicy),
		[]byte(`coordinator:
  provider_id: project-provider
  model: project-model
`),
		0o600,
	))

	store, err := NewPolicyStoreAt(filepath.Join(tmp, "global.yaml"))
	testutil.FailErr(t, "open policy store", err)
	got := mustPolicyOverlay(t, store, SettingsScopeProject, projectDir)
	if got.Coordinator.ProviderID != "project-provider" {
		t.Fatalf("project coordinator = %+v", got.Coordinator)
	}

	canonicalProjectDir, err := filepath.EvalSymlinks(projectDir)
	testutil.FailErr(t, "canonicalize project path", err)
	wantScope := "project " + canonicalProjectDir
	scopes := store.ProviderReferenceScopes("project-provider")
	if len(scopes) != 1 || scopes[0] != wantScope {
		t.Fatalf("reference scopes = %v, want [%q]", scopes, wantScope)
	}
}

func TestProjectPolicyReloadsDiskChanges(t *testing.T) {
	projectDir := t.TempDir()
	overlayDir := filepath.Join(projectDir, settingsoverlay.DirName())
	testutil.FailErr(t, "mkdir project overlay", os.MkdirAll(overlayDir, 0o700))
	path := filepath.Join(overlayDir, settingsoverlay.BasenameModelPolicy)
	write := func(provider string) {
		t.Helper()
		data := []byte("coordinator:\n  provider_id: " + provider + "\n  model: model\n")
		testutil.FailErr(t, "write project policy", os.WriteFile(path, data, 0o600))
	}
	write("first")

	configtest.Overlay(t, map[config.Rel]string{config.ModelPolicy: emptyModelPolicyYAML})
	store, err := NewPolicyStoreAt(filepath.Join(t.TempDir(), "model-policy.yaml"))
	testutil.FailErr(t, "create policy store", err)
	if got := mustPolicyOverlay(t, store, SettingsScopeProject, projectDir).Coordinator.ProviderID; got != "first" {
		t.Fatalf("first provider = %q", got)
	}

	write("second")
	if got := mustPolicyOverlay(t, store, SettingsScopeProject, projectDir).Coordinator.ProviderID; got != "second" {
		t.Fatalf("updated provider = %q", got)
	}
	testutil.FailErr(t, "write malformed project policy", os.WriteFile(path, []byte("coordinator: ["), 0o600))
	if _, err := store.Get(SettingsScopeProject, projectDir); err == nil {
		t.Fatal("malformed project update was ignored")
	}
	testutil.FailErr(t, "remove project policy", os.Remove(path))
	if policyHasContent(mustPolicyOverlay(t, store, SettingsScopeProject, projectDir)) {
		t.Fatal("removed project policy remained cached")
	}
	if scopes := store.ProviderReferenceScopes("second"); len(scopes) != 0 {
		t.Fatalf("removed project references = %v", scopes)
	}
}

func TestProjectPolicyReturnsConfigurationErrors(t *testing.T) {
	projectDir := t.TempDir()
	overlayDir := filepath.Join(projectDir, settingsoverlay.DirName())
	testutil.FailErr(t, "mkdir project overlay", os.MkdirAll(overlayDir, 0o700))
	testutil.FailErr(t, "write malformed project policy", os.WriteFile(
		filepath.Join(overlayDir, settingsoverlay.BasenameModelPolicy),
		[]byte("coordinator: ["),
		0o600,
	))
	configtest.Overlay(t, map[config.Rel]string{config.ModelPolicy: emptyModelPolicyYAML})
	store, err := NewPolicyStoreAt(filepath.Join(t.TempDir(), "model-policy.yaml"))
	testutil.FailErr(t, "create policy store", err)

	if _, err := store.Get(SettingsScopeProject, projectDir); err == nil {
		t.Fatal("effective policy ignored malformed project configuration")
	}
	if _, err := store.Overlay(SettingsScopeProject, projectDir); err == nil {
		t.Fatal("policy overlay ignored malformed project configuration")
	}
	if _, err := NewStaticModelRouter(store).WithScope(SettingsScopeProject, projectDir).Coordinator(t.Context()); err == nil {
		t.Fatal("model router ignored malformed project configuration")
	}
}

// The wire selection is always concrete.
func TestModelPolicyToDTONormalizesSelection(t *testing.T) {
	dto := ModelPolicyToDTO(ModelPolicy{})
	if dto.AgentPool.Selection != string(PoolSelectionRoundRobin) {
		t.Fatalf("empty policy selection = %q, want %q",
			dto.AgentPool.Selection, PoolSelectionRoundRobin)
	}
	if dto.AgentPool.Models == nil {
		t.Fatal("models must serialize as [], not null")
	}
	set := ModelPolicyToDTO(ModelPolicy{AgentPool: AgentPool{Selection: PoolSelectionFirst}})
	if set.AgentPool.Selection != string(PoolSelectionFirst) {
		t.Fatalf("explicit selection = %q, want %q",
			set.AgentPool.Selection, PoolSelectionFirst)
	}
}

func TestRouterOverlayRootsUsesProjectPolicy(t *testing.T) {
	tmp := t.TempDir()
	global := filepath.Join(tmp, "global.yaml")
	projectDir := filepath.Join(tmp, "proj")
	if err := os.MkdirAll(filepath.Join(projectDir, settingsoverlay.DirName()), 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	configtest.Overlay(t, map[config.Rel]string{config.ModelPolicy: emptyModelPolicyYAML})
	store, err := NewPolicyStoreAt(global)
	testutil.FailErr(t, "NewPolicyStoreAt", err)
	testutil.FailErr(t, "PutGlobal", store.PutGlobal(ModelPolicy{
		Coordinator: ModelRef{ProviderID: "openai", Model: "gpt-global"},
	}))
	testutil.FailErr(t, "PutProject", store.PutProject(projectDir, ModelPolicy{
		Coordinator: ModelRef{ProviderID: "openai", Model: "gpt-project"},
	}))
	router := NewStaticModelRouter(store)
	globalSel, err := router.WithOverlayRoots(nil).Coordinator(t.Context())
	testutil.FailErr(t, "global coordinator", err)
	if globalSel.Model != "gpt-global" {
		t.Fatalf("global model = %s", globalSel.Model)
	}
	ignored, err := router.WithScope(SettingsScopeGlobal, projectDir).Coordinator(t.Context())
	testutil.FailErr(t, "global+dir coordinator", err)
	if ignored.Model != "gpt-global" {
		t.Fatalf("Global+dir must not merge overlay: %s", ignored.Model)
	}
	projectSel, err := router.WithOverlayRoots([]string{projectDir}).Coordinator(t.Context())
	testutil.FailErr(t, "project coordinator", err)
	if projectSel.Model != "gpt-project" {
		t.Fatalf("overlay model = %s want gpt-project", projectSel.Model)
	}
}
