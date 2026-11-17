package security

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/google/uuid"
	"github.com/lycaon/lycaon/internal/extpacks"
	"github.com/lycaon/lycaon/internal/scan"
	scancatalog "github.com/lycaon/lycaon/internal/scan/catalog"
	scanoutput "github.com/lycaon/lycaon/internal/scan/output"
	"github.com/lycaon/lycaon/internal/testutil/gittest"
	wire "github.com/lycaon/lycaon/pkg/api"
)

// Pack identities the host recipes install. The released pack is a tagged
// local Git repository, the linked pack a development folder.
const (
	hostRecipeReleasedPack  = "conformance/released"
	hostRecipeLinkedPack    = "conformance/linked"
	hostRecipeSuggestedPack = "conformance/suggested"
	hostRecipeMetaPack      = "conformance/kit"
	hostRecipeMetaLeaf      = "conformance/kit-leaf"
	hostRecipeProfile       = "conformance"
	hostRecipeDetectionPack = "conformance-pack"
)

// hostRecipes cover device configuration: extensions, providers, MCP,
// scanners, detection packs, settings, grants, history and web research.
var hostRecipes = map[string]successRecipe{
	// Extensions.
	"installExtensionPack":         {stage: stageCreate, prepare: hostRecipeInstallPack},
	"reloadExtensionPack":          {stage: stageCreate, prepare: hostRecipeReloadPack},
	"getExtensionPackUpdate":       {prepare: hostRecipeGetPackUpdate},
	"applyExtensionPackUpdate":     {stage: stageChange, prepare: hostRecipeApplyPackUpdate},
	"applyExtensionPackProfile":    {stage: stageChange, prepare: hostRecipeApplyProfile},
	"updateExtensionPack":          {stage: stageChange, prepare: hostRecipeUpdatePack},
	"updateExtensionConfiguration": {stage: stageChange, prepare: hostRecipeUpdateConfiguration},
	"getExtensionUnit":             {prepare: hostRecipeGetUnit},
	"updateExtensionUnit":          {stage: stageChange, prepare: hostRecipeUpdateUnit},
	"deleteExtensionPack":          {stage: stageDelete, prepare: hostRecipeDeletePack},
	"installExtensionMetaPack":     {stage: stageCreate, prepare: hostRecipeInstallMetaPack},
	"applyExtensionMetaPack":       {stage: stageChange, prepare: hostRecipeApplyMetaPack},
	"updateExtensionMetaPack":      {stage: stageChange, prepare: hostRecipeUpdateMetaPack},
	"deleteExtensionMetaPack":      {stage: stageDelete, prepare: hostRecipeDeleteMetaPack},
	"acceptExtensionSuggestions":   {stage: stageCreate, prepare: hostRecipeAcceptSuggestions},

	// Model providers.
	"createProvider":           {stage: stageCreate, prepare: hostRecipeCreateProvider},
	"refreshProviderModels":    {stage: stageChange, prepare: hostRecipeRefreshModels},
	"deleteProviderCredential": {stage: stageDelete, prepare: hostRecipeDeleteCredential},

	// MCP.
	"createMcpProvider":    {stage: stageCreate, prepare: hostRecipeCreateMCPProvider},
	"listMcpProviderTools": {prepare: hostRecipeListMCPTools},
	"startMcpOAuth":        {stage: stageCreate, prepare: hostRecipeStartOAuth},
	"completeMcpOAuth":     {stage: stageChange, prepare: hostRecipeCompleteOAuth},

	// Scanners and scans.
	"createScanner":      {stage: stageCreate, prepare: hostRecipeCreateScanner},
	"updateScanner":      {stage: stageChange, prepare: hostRecipeUpdateScanner},
	"replaceScannerSlot": {stage: stageChange, prepare: hostRecipeReplaceSlot},
	"deleteScanner":      {stage: stageDelete, prepare: hostRecipeDeleteScanner},
	"listCodeScans":      {seed: hostRecipeSeedScans},
	"queryCodeScan":      {stage: stageChange, prepare: hostRecipeQueryScan},

	// Detection packs.
	"importDetectionPack": {stage: stageCreate, prepare: hostRecipeImportDetectionPack},
	"deleteDetectionPack": {stage: stageDelete, prepare: hostRecipeDeleteDetectionPack},

	// Settings and grants.
	"updateTrustSettings":  {stage: stageChange, prepare: hostRecipeUpdateTrust},
	"updateVerifySettings": {stage: stageChange, prepare: hostRecipeUpdateVerify},
	"createApprovalGrant":  {stage: stageCreate, prepare: hostRecipeCreateGrant},
	"resolveSocketGrant":   {stage: stageCreate, prepare: hostRecipeResolveSocket},

	// History storage.
	"previewHistoryRetention": {stage: stageChange, prepare: hostRecipePreviewHistory},
	"updateHistoryStorage":    {stage: stageChange, prepare: hostRecipeUpdateHistory},
	"pruneHistory":            {stage: stageChange, prepare: hostRecipePruneHistory},
	"createHistoryProtection": {stage: stageCreate, prepare: hostRecipeCreateProtection},
	"deleteHistoryProtection": {stage: stageDelete, prepare: hostRecipeDeleteProtection},

	// Web research.
	"updateWebResearchSettings": {stage: stageChange, prepare: hostRecipeUpdateResearchSettings},
	"updateWebResearchProvider": {stage: stageChange, prepare: hostRecipeUpdateResearchProvider},

	// Project removal read; its create runs in the host-wide stage.
	"getProjectRemoval": {prepare: hostRecipeGetProjectRemoval},
}

// hostRecipeMemo holds what one sweep's recipes share, so each fixture is
// made once per sweep.
var hostRecipeMemo = struct {
	sync.Mutex
	values map[*conformanceSweep]map[string]string
}{values: map[*conformanceSweep]map[string]string{}}

func hostRecipeT(s *conformanceSweep) (*testing.T, error) {
	if s.host == nil || s.fixture.t == nil {
		return nil, errors.New("host recipes need the production-wired host")
	}
	return s.fixture.t, nil
}

func hostRecipeRecall(s *conformanceSweep, key string) (string, bool) {
	hostRecipeMemo.Lock()
	defer hostRecipeMemo.Unlock()
	value, ok := hostRecipeMemo.values[s][key]
	return value, ok
}

func hostRecipeRemember(s *conformanceSweep, key, value string) {
	hostRecipeMemo.Lock()
	defer hostRecipeMemo.Unlock()
	if hostRecipeMemo.values[s] == nil {
		hostRecipeMemo.values[s] = map[string]string{}
		if s.fixture.t != nil {
			s.fixture.t.Cleanup(func() {
				hostRecipeMemo.Lock()
				defer hostRecipeMemo.Unlock()
				delete(hostRecipeMemo.values, s)
			})
		}
	}
	hostRecipeMemo.values[s][key] = value
}

// hostRecipeOnce returns key's value, making it the first time.
func hostRecipeOnce(s *conformanceSweep, key string, build func() (string, error)) (string, error) {
	if value, ok := hostRecipeRecall(s, key); ok {
		return value, nil
	}
	value, err := build()
	if err != nil {
		return "", err
	}
	hostRecipeRemember(s, key, value)
	return value, nil
}

func hostRecipeID(prefix string) string {
	return prefix + "-" + uuid.NewString()[:8]
}

func hostRecipeObjects(answer map[string]any, key string) []map[string]any {
	list, _ := answer[key].([]any)
	out := make([]map[string]any, 0, len(list))
	for _, item := range list {
		if row, ok := item.(map[string]any); ok {
			out = append(out, row)
		}
	}
	return out
}

func hostRecipeWrite(path, body string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return err
	}
	return os.WriteFile(path, []byte(body), 0o600)
}

func hostRecipeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

// --- Extensions ---------------------------------------------------------

func hostRecipeCatalog(ctx context.Context, s *conformanceSweep) (map[string]any, error) {
	return s.prerequisite(ctx, "getExtensionsCatalog", successInputs{})
}

// hostRecipeRevision is the extension-state revision every mutation names.
func hostRecipeRevision(ctx context.Context, s *conformanceSweep) (string, error) {
	catalog, err := hostRecipeCatalog(ctx, s)
	if err != nil {
		return "", err
	}
	revision, _ := catalog["revision"].(string)
	if revision == "" {
		return "", errors.New("the extensions catalog names no revision")
	}
	return revision, nil
}

// hostRecipeWritePack writes a guidance-only pack, with a profile that
// enables the pack itself when profile is set.
func hostRecipeWritePack(dir, id, version string, profile bool) error {
	leaf := strings.ReplaceAll(id, "/", "-")
	manifest := "manifest_version: 1\nid: " + id + "\nname: " + leaf + "\nversion: " + version +
		"\ncompatibility:\n  extension_api: \"^1.0.0\"\n"
	if err := hostRecipeWrite(filepath.Join(dir, "extension.yaml"), manifest); err != nil {
		return err
	}
	if err := hostRecipeWrite(filepath.Join(dir, "guidance", leaf+".md"), "# "+leaf+" "+version+"\n"); err != nil {
		return err
	}
	if !profile {
		return nil
	}
	return hostRecipeWrite(filepath.Join(dir, "profiles", hostRecipeProfile+".yaml"),
		"name: "+hostRecipeProfile+"\nenable:\n  - "+id+"\n")
}

// hostRecipeReleaseRepo is a Git repository whose v1.0.0 tag releases id.
func hostRecipeReleaseRepo(s *conformanceSweep, id string) (string, error) {
	return hostRecipeOnce(s, "repo:"+id, func() (string, error) {
		t, err := hostRecipeT(s)
		if err != nil {
			return "", err
		}
		dir := filepath.Join(t.TempDir(), strings.ReplaceAll(id, "/", "-"))
		if err := hostRecipeWritePack(dir, id, "1.0.0", true); err != nil {
			return "", err
		}
		gittest.InitCommit(t, dir, "release 1.0.0")
		gittest.Run(t, dir, "tag", "v1.0.0")
		return dir, nil
	})
}

func hostRecipeLinkedDir(s *conformanceSweep) (string, error) {
	return hostRecipeOnce(s, "linked-dir", func() (string, error) {
		t, err := hostRecipeT(s)
		if err != nil {
			return "", err
		}
		dir := filepath.Join(t.TempDir(), "linked")
		return dir, hostRecipeWritePack(dir, hostRecipeLinkedPack, "1.0.0", false)
	})
}

func hostRecipeMetaSuite(s *conformanceSweep) (string, error) {
	return hostRecipeOnce(s, "meta-suite", func() (string, error) {
		t, err := hostRecipeT(s)
		if err != nil {
			return "", err
		}
		dir := filepath.Join(t.TempDir(), "suite")
		if err := hostRecipeWritePack(filepath.Join(dir, "leaf"), hostRecipeMetaLeaf, "1.0.0", false); err != nil {
			return "", err
		}
		meta := "manifest_version: 1\nid: " + hostRecipeMetaPack + "\nname: Kit\nversion: \"1.0.0\"\n" +
			"compatibility:\n  extension_api: \"^1.0.0\"\nmembers:\n  - " + hostRecipeMetaLeaf +
			"\nconflicts_with: []\nextends: []\n"
		return dir, hostRecipeWrite(filepath.Join(dir, "meta.yaml"), meta)
	})
}

func hostRecipeReleasedSource(s *conformanceSweep) (string, error) {
	repo, err := hostRecipeReleaseRepo(s, hostRecipeReleasedPack)
	return "file://" + repo, err
}

func hostRecipeLinkedSource(s *conformanceSweep) (string, error) {
	dir, err := hostRecipeLinkedDir(s)
	return "path:" + dir, err
}

// hostRecipeEnsurePack installs id from source unless the catalog lists it.
func hostRecipeEnsurePack(ctx context.Context, s *conformanceSweep, id string, source func(*conformanceSweep) (string, error)) error {
	catalog, err := hostRecipeCatalog(ctx, s)
	if err != nil {
		return err
	}
	for _, pack := range hostRecipeObjects(catalog, "packs") {
		if pack["id"] == id {
			return nil
		}
	}
	src, err := source(s)
	if err != nil {
		return err
	}
	revision, _ := catalog["revision"].(string)
	_, err = s.prerequisite(ctx, "installExtensionPack", successInputs{fields: map[string]any{
		"source": src, "expected_revision": revision,
	}})
	return err
}

func hostRecipeEnsureMetaPack(ctx context.Context, s *conformanceSweep) error {
	catalog, err := hostRecipeCatalog(ctx, s)
	if err != nil {
		return err
	}
	for _, meta := range hostRecipeObjects(catalog, "meta_packs") {
		if meta["id"] == hostRecipeMetaPack {
			return nil
		}
	}
	suite, err := hostRecipeMetaSuite(s)
	if err != nil {
		return err
	}
	revision, _ := catalog["revision"].(string)
	_, err = s.prerequisite(ctx, "installExtensionMetaPack", successInputs{fields: map[string]any{
		"source": "path:" + suite, "expected_revision": revision,
	}})
	return err
}

// hostRecipePackRevision installs id when needed and returns the revision
// that follows.
func hostRecipePackRevision(ctx context.Context, s *conformanceSweep, id string, source func(*conformanceSweep) (string, error)) (string, error) {
	if err := hostRecipeEnsurePack(ctx, s, id, source); err != nil {
		return "", err
	}
	return hostRecipeRevision(ctx, s)
}

func hostRecipeInstallPack(ctx context.Context, s *conformanceSweep, _ *conformanceOp) (successInputs, error) {
	source, err := hostRecipeReleasedSource(s)
	if err != nil {
		return successInputs{}, err
	}
	revision, err := hostRecipeRevision(ctx, s)
	if err != nil {
		return successInputs{}, err
	}
	return successInputs{fields: map[string]any{"source": source, "expected_revision": revision}}, nil
}

func hostRecipeReloadPack(ctx context.Context, s *conformanceSweep, _ *conformanceOp) (successInputs, error) {
	revision, err := hostRecipePackRevision(ctx, s, hostRecipeLinkedPack, hostRecipeLinkedSource)
	if err != nil {
		return successInputs{}, err
	}
	return successInputs{params: map[string]string{"pack_id": hostRecipeLinkedPack},
		fields: map[string]any{"expected_revision": revision}}, nil
}

// hostRecipeGetPackUpdate reads the released pack; before it is installed
// the read answers not found, which is not a success.
func hostRecipeGetPackUpdate(context.Context, *conformanceSweep, *conformanceOp) (successInputs, error) {
	return successInputs{params: map[string]string{"pack_id": hostRecipeReleasedPack}}, nil
}

// hostRecipeApplyPackUpdate tags a 1.1.0 release so the update has a
// candidate to publish.
func hostRecipeApplyPackUpdate(ctx context.Context, s *conformanceSweep, _ *conformanceOp) (successInputs, error) {
	if err := hostRecipeEnsurePack(ctx, s, hostRecipeReleasedPack, hostRecipeReleasedSource); err != nil {
		return successInputs{}, err
	}
	_, err := hostRecipeOnce(s, "released-1.1.0", func() (string, error) {
		repo, err := hostRecipeReleaseRepo(s, hostRecipeReleasedPack)
		if err != nil {
			return "", err
		}
		if err := hostRecipeWritePack(repo, hostRecipeReleasedPack, "1.1.0", true); err != nil {
			return "", err
		}
		gittest.CommitAll(s.fixture.t, repo, "release 1.1.0")
		gittest.Run(s.fixture.t, repo, "tag", "v1.1.0")
		return "tagged", nil
	})
	if err != nil {
		return successInputs{}, err
	}
	revision, err := hostRecipeRevision(ctx, s)
	if err != nil {
		return successInputs{}, err
	}
	return successInputs{params: map[string]string{"pack_id": hostRecipeReleasedPack},
		fields: map[string]any{"expected_revision": revision}}, nil
}

func hostRecipeApplyProfile(ctx context.Context, s *conformanceSweep, _ *conformanceOp) (successInputs, error) {
	revision, err := hostRecipePackRevision(ctx, s, hostRecipeReleasedPack, hostRecipeReleasedSource)
	if err != nil {
		return successInputs{}, err
	}
	return successInputs{
		params: map[string]string{"pack_id": hostRecipeReleasedPack, "profile_name": hostRecipeProfile},
		fields: map[string]any{"expected_revision": revision},
	}, nil
}

// hostRecipeUpdatePack disables the linked pack, which no later recipe needs
// enabled.
func hostRecipeUpdatePack(ctx context.Context, s *conformanceSweep, _ *conformanceOp) (successInputs, error) {
	revision, err := hostRecipePackRevision(ctx, s, hostRecipeLinkedPack, hostRecipeLinkedSource)
	if err != nil {
		return successInputs{}, err
	}
	return successInputs{params: map[string]string{"pack_id": hostRecipeLinkedPack},
		fields: map[string]any{"enabled": false, "expected_revision": revision}}, nil
}

// hostRecipeUpdateConfiguration sends an empty block, which clears the
// pack's configuration whatever properties it declares.
func hostRecipeUpdateConfiguration(ctx context.Context, s *conformanceSweep, _ *conformanceOp) (successInputs, error) {
	revision, err := hostRecipePackRevision(ctx, s, hostRecipeReleasedPack, hostRecipeReleasedSource)
	if err != nil {
		return successInputs{}, err
	}
	return successInputs{fields: map[string]any{
		"packs":             map[string]any{hostRecipeReleasedPack: map[string]any{}},
		"expected_revision": revision,
	}}, nil
}

func hostRecipeGetUnit(ctx context.Context, s *conformanceSweep, _ *conformanceOp) (successInputs, error) {
	catalog, err := hostRecipeCatalog(ctx, s)
	if err != nil {
		return successInputs{}, err
	}
	for _, unit := range hostRecipeObjects(catalog, "units") {
		if id, _ := unit["id"].(string); id != "" {
			return successInputs{params: map[string]string{"unit_id": id}}, nil
		}
	}
	return successInputs{}, errors.New("the extensions catalog lists no unit")
}

// hostRecipeUpdateUnit disables the released pack's guidance unit.
func hostRecipeUpdateUnit(ctx context.Context, s *conformanceSweep, _ *conformanceOp) (successInputs, error) {
	if err := hostRecipeEnsurePack(ctx, s, hostRecipeReleasedPack, hostRecipeReleasedSource); err != nil {
		return successInputs{}, err
	}
	catalog, err := hostRecipeCatalog(ctx, s)
	if err != nil {
		return successInputs{}, err
	}
	revision, _ := catalog["revision"].(string)
	for _, unit := range hostRecipeObjects(catalog, "units") {
		if unit["winner_pack_id"] == hostRecipeReleasedPack {
			id, _ := unit["id"].(string)
			return successInputs{params: map[string]string{"unit_id": id},
				fields: map[string]any{"enabled": false, "expected_revision": revision}}, nil
		}
	}
	return successInputs{}, fmt.Errorf("pack %s contributes no unit", hostRecipeReleasedPack)
}

func hostRecipeDeletePack(ctx context.Context, s *conformanceSweep, _ *conformanceOp) (successInputs, error) {
	revision, err := hostRecipePackRevision(ctx, s, hostRecipeReleasedPack, hostRecipeReleasedSource)
	if err != nil {
		return successInputs{}, err
	}
	return successInputs{params: map[string]string{"pack_id": hostRecipeReleasedPack},
		query: map[string]string{"expected_revision": revision}}, nil
}

func hostRecipeInstallMetaPack(ctx context.Context, s *conformanceSweep, _ *conformanceOp) (successInputs, error) {
	suite, err := hostRecipeMetaSuite(s)
	if err != nil {
		return successInputs{}, err
	}
	revision, err := hostRecipeRevision(ctx, s)
	if err != nil {
		return successInputs{}, err
	}
	return successInputs{fields: map[string]any{"source": "path:" + suite, "expected_revision": revision}}, nil
}

func hostRecipeMetaRevision(ctx context.Context, s *conformanceSweep) (string, error) {
	if err := hostRecipeEnsureMetaPack(ctx, s); err != nil {
		return "", err
	}
	return hostRecipeRevision(ctx, s)
}

func hostRecipeApplyMetaPack(ctx context.Context, s *conformanceSweep, _ *conformanceOp) (successInputs, error) {
	revision, err := hostRecipeMetaRevision(ctx, s)
	if err != nil {
		return successInputs{}, err
	}
	return successInputs{params: map[string]string{"meta_pack_id": hostRecipeMetaPack},
		fields: map[string]any{"expected_revision": revision}}, nil
}

func hostRecipeUpdateMetaPack(ctx context.Context, s *conformanceSweep, _ *conformanceOp) (successInputs, error) {
	revision, err := hostRecipeMetaRevision(ctx, s)
	if err != nil {
		return successInputs{}, err
	}
	return successInputs{params: map[string]string{"meta_pack_id": hostRecipeMetaPack},
		fields: map[string]any{"enabled": false, "expected_revision": revision}}, nil
}

func hostRecipeDeleteMetaPack(ctx context.Context, s *conformanceSweep, _ *conformanceOp) (successInputs, error) {
	revision, err := hostRecipeMetaRevision(ctx, s)
	if err != nil {
		return successInputs{}, err
	}
	return successInputs{params: map[string]string{"meta_pack_id": hostRecipeMetaPack},
		query: map[string]string{"expected_revision": revision}}, nil
}

// hostRecipeAcceptSuggestions has the fixture project suggest a released
// pack, then accepts the proposal it reviewed.
func hostRecipeAcceptSuggestions(ctx context.Context, s *conformanceSweep, _ *conformanceOp) (successInputs, error) {
	if _, err := hostRecipeT(s); err != nil {
		return successInputs{}, err
	}
	repo, err := hostRecipeReleaseRepo(s, hostRecipeSuggestedPack)
	if err != nil {
		return successInputs{}, err
	}
	manifest := "format: 1\nsuggest:\n  - id: " + hostRecipeSuggestedPack + "\n    source: file://" + repo + "\n"
	if err := hostRecipeWrite(extpacks.ProjectDesiredPath(s.fixture.projectDir), manifest); err != nil {
		return successInputs{}, err
	}
	reviewed, err := s.prerequisite(ctx, "getExtensionSuggestions", successInputs{})
	if err != nil {
		return successInputs{}, err
	}
	suggestionRevision, _ := reviewed["suggestion_revision"].(string)
	revision, err := hostRecipeRevision(ctx, s)
	if err != nil {
		return successInputs{}, err
	}
	return successInputs{fields: map[string]any{
		"pack_ids":                     []string{hostRecipeSuggestedPack},
		"expected_revision":            revision,
		"expected_suggestion_revision": suggestionRevision,
	}}, nil
}

// --- Model providers ----------------------------------------------------

// hostRecipeModelServer lists one model the way a compatible provider does.
func hostRecipeModelServer(s *conformanceSweep) (string, error) {
	return hostRecipeOnce(s, "model-server", func() (string, error) {
		t, err := hostRecipeT(s)
		if err != nil {
			return "", err
		}
		mux := http.NewServeMux()
		mux.HandleFunc("GET /models", func(w http.ResponseWriter, _ *http.Request) {
			hostRecipeJSON(w, http.StatusOK, map[string]any{"data": []any{map[string]any{"id": "conformance-model"}}})
		})
		srv := httptest.NewServer(mux)
		t.Cleanup(srv.Close)
		return srv.URL, nil
	})
}

func hostRecipeProviderFields(s *conformanceSweep, requiresKey bool) (map[string]any, error) {
	base, err := hostRecipeModelServer(s)
	if err != nil {
		return nil, err
	}
	return map[string]any{"id": hostRecipeID("conformance"), "base_url": base, "requires_api_key": requiresKey}, nil
}

func hostRecipeCreateProvider(_ context.Context, s *conformanceSweep, _ *conformanceOp) (successInputs, error) {
	fields, err := hostRecipeProviderFields(s, false)
	return successInputs{fields: fields}, err
}

func hostRecipeRefreshModels(ctx context.Context, s *conformanceSweep, _ *conformanceOp) (successInputs, error) {
	id, err := hostRecipeOnce(s, "listed-provider", func() (string, error) {
		fields, err := hostRecipeProviderFields(s, false)
		if err != nil {
			return "", err
		}
		_, err = s.prerequisite(ctx, "createProvider", successInputs{fields: fields})
		return fields["id"].(string), err
	})
	return successInputs{params: map[string]string{"provider_id": id}}, err
}

// hostRecipeDeleteCredential stores a key on a provider of its own, since the
// delete stage removes the shared fixture provider first.
func hostRecipeDeleteCredential(ctx context.Context, s *conformanceSweep, _ *conformanceOp) (successInputs, error) {
	fields, err := hostRecipeProviderFields(s, true)
	if err != nil {
		return successInputs{}, err
	}
	if _, err := s.prerequisite(ctx, "createProvider", successInputs{fields: fields}); err != nil {
		return successInputs{}, err
	}
	params := map[string]string{"provider_id": fields["id"].(string)}
	key := s.secrets.issue("deleteProviderCredential.api_key")
	if _, err := s.prerequisite(ctx, "replaceProviderCredential", successInputs{
		params: params, fields: map[string]any{"api_key": key},
	}); err != nil {
		return successInputs{}, err
	}
	return successInputs{params: params}, nil
}

// --- MCP ----------------------------------------------------------------

// hostRecipeCreateMCPProvider adds an enabled custom provider; the harness
// connector answers every provider, so enabling connects nothing real. The
// first one created serves tool listing.
func hostRecipeCreateMCPProvider(_ context.Context, s *conformanceSweep, _ *conformanceOp) (successInputs, error) {
	id := hostRecipeID("conformance-mcp")
	if _, ok := hostRecipeRecall(s, "mcp-tools-provider"); !ok {
		hostRecipeRemember(s, "mcp-tools-provider", id)
	}
	return successInputs{fields: map[string]any{
		"source": "custom", "id": id, "url": "http://127.0.0.1:9/mcp", "enabled": true,
	}}, nil
}

// hostRecipeListMCPTools lists the first created conformance provider; before
// it exists the read answers not found.
func hostRecipeListMCPTools(_ context.Context, s *conformanceSweep, _ *conformanceOp) (successInputs, error) {
	id, ok := hostRecipeRecall(s, "mcp-tools-provider")
	if !ok {
		id = "conformance-mcp-absent"
	}
	return successInputs{params: map[string]string{"provider_id": id}}, nil
}

// hostRecipeOAuthServer is an authorization server with dynamic client
// registration and a token endpoint; the MCP endpoint itself is absent, so
// discovery falls back to the server root.
func hostRecipeOAuthServer(s *conformanceSweep) (string, error) {
	return hostRecipeOnce(s, "oauth-server", func() (string, error) {
		t, err := hostRecipeT(s)
		if err != nil {
			return "", err
		}
		var srv *httptest.Server
		mux := http.NewServeMux()
		mux.HandleFunc("GET /.well-known/oauth-authorization-server", func(w http.ResponseWriter, _ *http.Request) {
			hostRecipeJSON(w, http.StatusOK, map[string]any{
				"issuer":                           srv.URL,
				"authorization_endpoint":           srv.URL + "/authorize",
				"token_endpoint":                   srv.URL + "/token",
				"registration_endpoint":            srv.URL + "/register",
				"response_types_supported":         []string{"code"},
				"grant_types_supported":            []string{"authorization_code", "refresh_token"},
				"code_challenge_methods_supported": []string{"S256"},
			})
		})
		mux.HandleFunc("POST /register", func(w http.ResponseWriter, _ *http.Request) {
			hostRecipeJSON(w, http.StatusCreated, map[string]any{"client_id": "conformance-client"})
		})
		mux.HandleFunc("POST /token", func(w http.ResponseWriter, _ *http.Request) {
			hostRecipeJSON(w, http.StatusOK, map[string]any{
				"access_token": "conformance-access", "token_type": "Bearer", "expires_in": 3600,
			})
		})
		srv = httptest.NewServer(mux)
		t.Cleanup(srv.Close)
		return srv.URL, nil
	})
}

func hostRecipeOAuthProvider(ctx context.Context, s *conformanceSweep) (string, error) {
	return hostRecipeOnce(s, "oauth-provider", func() (string, error) {
		base, err := hostRecipeOAuthServer(s)
		if err != nil {
			return "", err
		}
		id := hostRecipeID("conformance-oauth")
		_, err = s.prerequisite(ctx, "createMcpProvider", successInputs{fields: map[string]any{
			"source": "custom", "id": id, "url": base + "/mcp", "enabled": false,
		}})
		return id, err
	})
}

func hostRecipeStartOAuth(ctx context.Context, s *conformanceSweep, _ *conformanceOp) (successInputs, error) {
	id, err := hostRecipeOAuthProvider(ctx, s)
	return successInputs{params: map[string]string{"provider_id": id}}, err
}

// hostRecipeCompleteOAuth starts a fresh authorization and redeems its state.
func hostRecipeCompleteOAuth(ctx context.Context, s *conformanceSweep, _ *conformanceOp) (successInputs, error) {
	id, err := hostRecipeOAuthProvider(ctx, s)
	if err != nil {
		return successInputs{}, err
	}
	params := map[string]string{"provider_id": id}
	started, err := s.prerequisite(ctx, "startMcpOAuth", successInputs{params: params})
	if err != nil {
		return successInputs{}, err
	}
	state, _ := started["state"].(string)
	return successInputs{params: params, fields: map[string]any{"code": "conformance-code", "state": state}}, nil
}

// --- Scanners and scans -------------------------------------------------

// hostRecipeScanners lists every scanner in the merged catalog.
func hostRecipeScanners(ctx context.Context, s *conformanceSweep) ([]map[string]any, error) {
	listed, err := s.prerequisite(ctx, "listScanners", successInputs{})
	if err != nil {
		return nil, err
	}
	return hostRecipeObjects(listed, "scanners"), nil
}

// hostRecipeCatalogScanner picks a scanner-catalog row: one already added
// when added is set, otherwise one no layer defines yet.
func hostRecipeCatalogScanner(ctx context.Context, s *conformanceSweep, added bool) (string, bool, error) {
	scanners, err := hostRecipeScanners(ctx, s)
	if err != nil {
		return "", false, err
	}
	defined := map[string]bool{}
	for _, row := range scanners {
		id, _ := row["id"].(string)
		defined[id] = true
	}
	listed, err := s.prerequisite(ctx, "listScannerCatalog", successInputs{})
	if err != nil {
		return "", false, err
	}
	for _, row := range hostRecipeObjects(listed, "scanners") {
		id, _ := row["id"].(string)
		isAdded, _ := row["added"].(bool)
		if id != "" && isAdded == added && (added || !defined[id]) {
			return id, true, nil
		}
	}
	return "", false, nil
}

func hostRecipeCreateScanner(ctx context.Context, s *conformanceSweep, _ *conformanceOp) (successInputs, error) {
	id, ok, err := hostRecipeCatalogScanner(ctx, s, false)
	if err != nil {
		return successInputs{}, err
	}
	if !ok {
		return successInputs{}, errors.New("every scanner-catalog row is already defined")
	}
	return successInputs{fields: map[string]any{"source": "catalog", "catalog_id": id}}, nil
}

// hostRecipeUserScanner returns a device scanner added from the catalog,
// adding one when none is.
func hostRecipeUserScanner(ctx context.Context, s *conformanceSweep) (string, error) {
	if id, ok, err := hostRecipeCatalogScanner(ctx, s, true); err != nil || ok {
		return id, err
	}
	in, err := hostRecipeCreateScanner(ctx, s, nil)
	if err != nil {
		return "", err
	}
	if _, err := s.prerequisite(ctx, "createScanner", in); err != nil {
		return "", err
	}
	return in.fields["catalog_id"].(string), nil
}

// hostRecipeUpdateScanner replaces the added scanner with its own complete
// catalog definition under a new label.
func hostRecipeUpdateScanner(ctx context.Context, s *conformanceSweep, _ *conformanceOp) (successInputs, error) {
	id, err := hostRecipeUserScanner(ctx, s)
	if err != nil {
		return successInputs{}, err
	}
	catalog, err := scancatalog.LoadScannerCatalog()
	if err != nil {
		return successInputs{}, err
	}
	entry, ok := catalog.ScannerEntryFor(id)
	if !ok {
		return successInputs{}, fmt.Errorf("scanner catalog has no row %s", id)
	}
	runtime := entry.RuntimePolicy()
	return successInputs{params: map[string]string{"scanner_id": id}, fields: map[string]any{
		"engine":        entry.Engine,
		"scope_kind":    entry.ScopeKind,
		"command":       entry.Command,
		"output_parser": entry.OutputParser,
		"categories":    entry.Categories,
		"label":         "Conformance scanner",
		"runtime": map[string]any{
			"soft_limit_ms": runtime.SoftLimitSec * 1000,
			"hard_limit_ms": runtime.HardLimitSec * 1000,
			"cpu_units":     runtime.CPUUnits,
			"parallelism":   runtime.Parallelism,
		},
	}}, nil
}

func hostRecipeDeleteScanner(ctx context.Context, s *conformanceSweep, _ *conformanceOp) (successInputs, error) {
	id, err := hostRecipeUserScanner(ctx, s)
	return successInputs{params: map[string]string{"scanner_id": id}}, err
}

// hostRecipeReplaceSlot reselects a slot's current scanner, so no scanner
// the sweep relies on is swapped out.
func hostRecipeReplaceSlot(ctx context.Context, s *conformanceSweep, _ *conformanceOp) (successInputs, error) {
	scanners, err := hostRecipeScanners(ctx, s)
	if err != nil {
		return successInputs{}, err
	}
	var fallback *successInputs
	for _, row := range scanners {
		id, _ := row["id"].(string)
		var categories []string
		listed, _ := row["categories"].([]any)
		for _, c := range listed {
			name, _ := c.(string)
			categories = append(categories, name)
		}
		slot := scancatalog.PrimaryCategory(categories)
		if id == "" || !scancatalog.IsSlotCategory(slot) {
			continue
		}
		in := successInputs{params: map[string]string{"category": slot}, fields: map[string]any{"scanner_id": id}}
		if enabled, _ := row["enabled"].(bool); enabled {
			return in, nil
		}
		if fallback == nil {
			fallback = &in
		}
	}
	if fallback != nil {
		return *fallback, nil
	}
	return successInputs{}, errors.New("no scanner covers a scanner slot")
}

// hostRecipeDrainScans completes every queued scan with an empty result, as
// the scan runner would; the sweep does not start background runners.
func hostRecipeDrainScans(ctx context.Context, s *conformanceSweep) error {
	store := scan.NewSQLStore(s.host.DB)
	for range 64 {
		claimed, err := store.ClaimNext(ctx)
		if errors.Is(err, scan.ErrNoPendingScans) {
			return nil
		}
		if err != nil {
			return err
		}
		if _, err := store.MarkComplete(ctx, claimed, &scanoutput.Result{}); err != nil {
			return err
		}
	}
	return errors.New("scans kept queueing")
}

func hostRecipeFullScan(ctx context.Context, s *conformanceSweep) error {
	params := map[string]string{"id": s.fixture.project.ID}
	if _, err := s.prerequisite(ctx, "startFullScan", successInputs{params: params}); err != nil {
		return err
	}
	return hostRecipeDrainScans(ctx, s)
}

// hostRecipeSeedScans runs two more completed full passes.
func hostRecipeSeedScans(ctx context.Context, s *conformanceSweep) error {
	if _, err := hostRecipeT(s); err != nil {
		return err
	}
	if err := hostRecipeDrainScans(ctx, s); err != nil {
		return err
	}
	for range 2 {
		if err := hostRecipeFullScan(ctx, s); err != nil {
			return err
		}
	}
	return nil
}

// hostRecipeQueryScan queries a completed scan of the fixture project.
func hostRecipeQueryScan(ctx context.Context, s *conformanceSweep, _ *conformanceOp) (successInputs, error) {
	if _, err := hostRecipeT(s); err != nil {
		return successInputs{}, err
	}
	params := map[string]string{"id": s.fixture.project.ID}
	complete := func() (string, error) {
		if err := hostRecipeDrainScans(ctx, s); err != nil {
			return "", err
		}
		page, err := s.prerequisite(ctx, "listCodeScans", successInputs{params: params})
		if err != nil {
			return "", err
		}
		for _, row := range hostRecipeObjects(page, "scans") {
			if row["status"] == string(wire.CodeScanStatusComplete) {
				id, _ := row["id"].(string)
				return id, nil
			}
		}
		return "", nil
	}
	id, err := complete()
	if err == nil && id == "" {
		if err = hostRecipeFullScan(ctx, s); err == nil {
			id, err = complete()
		}
	}
	if err == nil && id == "" {
		err = errors.New("no scan of the fixture project completed")
	}
	return successInputs{params: map[string]string{"id": s.fixture.project.ID, "scan_id": id}}, err
}

// --- Detection packs ----------------------------------------------------

func hostRecipeDetectionPackDir(s *conformanceSweep) (string, error) {
	return hostRecipeOnce(s, "detection-pack", func() (string, error) {
		t, err := hostRecipeT(s)
		if err != nil {
			return "", err
		}
		dir := filepath.Join(t.TempDir(), "detection-pack")
		if err := hostRecipeWrite(filepath.Join(dir, "pack.yaml"),
			"id: "+hostRecipeDetectionPack+"\nlabel: Conformance\ndescription: Conformance sweep rules\n"); err != nil {
			return "", err
		}
		rule := "title: Conformance binary\nid: " + uuid.NewString() + "\ndescription: conformance\n" +
			"logsource:\n  product: lycaon\n  service: tool_exec\nlevel: high\n" +
			"detection:\n  sel:\n    Image: conformance-binary\n  condition: sel\n"
		return dir, hostRecipeWrite(filepath.Join(dir, "rules", "conformance.yml"), rule)
	})
}

func hostRecipeImportDetectionPack(_ context.Context, s *conformanceSweep, _ *conformanceOp) (successInputs, error) {
	dir, err := hostRecipeDetectionPackDir(s)
	return successInputs{fields: map[string]any{"path": dir, "dry_run": false, "replace": true}}, err
}

func hostRecipeDeleteDetectionPack(ctx context.Context, s *conformanceSweep, _ *conformanceOp) (successInputs, error) {
	listed, err := s.prerequisite(ctx, "listDetectionPacks", successInputs{})
	if err != nil {
		return successInputs{}, err
	}
	present := false
	for _, pack := range hostRecipeObjects(listed, "packs") {
		present = present || pack["id"] == hostRecipeDetectionPack
	}
	if !present {
		in, err := hostRecipeImportDetectionPack(ctx, s, nil)
		if err != nil {
			return successInputs{}, err
		}
		if _, err := s.prerequisite(ctx, "importDetectionPack", in); err != nil {
			return successInputs{}, err
		}
	}
	return successInputs{params: map[string]string{"pack_id": hostRecipeDetectionPack}}, nil
}

// --- Settings and grants ------------------------------------------------

// hostRecipeUpdateTrust restates one surface's current switch.
func hostRecipeUpdateTrust(ctx context.Context, s *conformanceSweep, _ *conformanceOp) (successInputs, error) {
	current, err := s.prerequisite(ctx, "getTrustSettings", successInputs{})
	if err != nil {
		return successInputs{}, err
	}
	for _, surface := range hostRecipeObjects(current, "surfaces") {
		id, _ := surface["id"].(string)
		enabled, _ := surface["enabled"].(bool)
		if id != "" {
			return successInputs{fields: map[string]any{"enabled": map[string]any{id: enabled}}}, nil
		}
	}
	return successInputs{}, errors.New("trust settings list no surface")
}

func hostRecipeUpdateVerify(context.Context, *conformanceSweep, *conformanceOp) (successInputs, error) {
	return successInputs{fields: map[string]any{"test": "go test ./..."}}, nil
}

func hostRecipeCreateGrant(_ context.Context, s *conformanceSweep, _ *conformanceOp) (successInputs, error) {
	if _, err := hostRecipeT(s); err != nil {
		return successInputs{}, err
	}
	return successInputs{fields: map[string]any{
		"category":     string(wire.ApprovalGrantCategoryHost),
		"scope":        string(wire.ApprovalGrantScopeProject),
		"host_pattern": "example.com",
		"project_id":   s.fixture.project.ID,
	}}, nil
}

// hostRecipeSocket listens on an AF_UNIX socket under a short temporary
// path, within the platform's socket path limit.
func hostRecipeSocket(s *conformanceSweep) (string, error) {
	return hostRecipeOnce(s, "socket", func() (string, error) {
		t, err := hostRecipeT(s)
		if err != nil {
			return "", err
		}
		dir, err := os.MkdirTemp("", "pwsock")
		if err != nil {
			return "", err
		}
		t.Cleanup(func() { _ = os.RemoveAll(dir) })
		path := filepath.Join(dir, "c.sock")
		listener, err := net.Listen("unix", path)
		if err != nil {
			return "", err
		}
		t.Cleanup(func() { _ = listener.Close() })
		return path, nil
	})
}

func hostRecipeResolveSocket(_ context.Context, s *conformanceSweep, _ *conformanceOp) (successInputs, error) {
	path, err := hostRecipeSocket(s)
	return successInputs{fields: map[string]any{"socket_path": path}}, err
}

// --- History storage ----------------------------------------------------

func hostRecipePolicy(ctx context.Context, s *conformanceSweep) (any, error) {
	status, err := s.prerequisite(ctx, "getHistoryStorage", successInputs{})
	if err != nil {
		return nil, err
	}
	policy, ok := status["policy"].(map[string]any)
	if !ok {
		return nil, errors.New("history storage names no policy")
	}
	return policy, nil
}

// hostRecipeReviewed previews the current policy and returns the request
// that names the preview.
func hostRecipeReviewed(ctx context.Context, s *conformanceSweep) (successInputs, error) {
	policy, err := hostRecipePolicy(ctx, s)
	if err != nil {
		return successInputs{}, err
	}
	preview, err := s.prerequisite(ctx, "previewHistoryRetention", successInputs{fields: map[string]any{"policy": policy}})
	if err != nil {
		return successInputs{}, err
	}
	token, _ := preview["token"].(string)
	return successInputs{fields: map[string]any{"policy": policy, "preview_token": token}}, nil
}

func hostRecipePreviewHistory(ctx context.Context, s *conformanceSweep, _ *conformanceOp) (successInputs, error) {
	policy, err := hostRecipePolicy(ctx, s)
	return successInputs{fields: map[string]any{"policy": policy}}, err
}

func hostRecipeUpdateHistory(ctx context.Context, s *conformanceSweep, _ *conformanceOp) (successInputs, error) {
	return hostRecipeReviewed(ctx, s)
}

func hostRecipePruneHistory(ctx context.Context, s *conformanceSweep, _ *conformanceOp) (successInputs, error) {
	return hostRecipeReviewed(ctx, s)
}

func hostRecipeProtection(s *conformanceSweep) (map[string]any, error) {
	if _, err := hostRecipeT(s); err != nil {
		return nil, err
	}
	return map[string]any{"scope_type": "project", "scope_id": s.fixture.project.ID, "protected": true}, nil
}

func hostRecipeCreateProtection(_ context.Context, s *conformanceSweep, _ *conformanceOp) (successInputs, error) {
	fields, err := hostRecipeProtection(s)
	return successInputs{fields: fields}, err
}

// hostRecipeDeleteProtection protects the fixture project and removes that
// protection, whose id is its owner's.
func hostRecipeDeleteProtection(ctx context.Context, s *conformanceSweep, _ *conformanceOp) (successInputs, error) {
	fields, err := hostRecipeProtection(s)
	if err != nil {
		return successInputs{}, err
	}
	if _, err := s.prerequisite(ctx, "createHistoryProtection", successInputs{fields: fields}); err != nil {
		return successInputs{}, err
	}
	return successInputs{params: map[string]string{"protection_id": s.fixture.project.ID}}, nil
}

// --- Web research -------------------------------------------------------

func hostRecipeUpdateResearchSettings(context.Context, *conformanceSweep, *conformanceOp) (successInputs, error) {
	return successInputs{fields: map[string]any{"warming": false}}, nil
}

func hostRecipeUpdateResearchProvider(context.Context, *conformanceSweep, *conformanceOp) (successInputs, error) {
	return successInputs{fields: map[string]any{"config": map[string]any{}}}, nil
}

// --- Project removal and backup -----------------------------------------

// hostRecipeRemovalRequest assesses projectID and accepts its removal.
func hostRecipeRemovalRequest(ctx context.Context, s *conformanceSweep, projectID string) (successInputs, error) {
	params := map[string]string{"id": projectID}
	assessment, err := s.prerequisite(ctx, "assessProjectRemoval", successInputs{params: params})
	if err != nil {
		return successInputs{}, err
	}
	token, _ := assessment["assessment_token"].(string)
	return successInputs{params: params, fields: map[string]any{
		"operation_id":      uuid.NewString(),
		"assessment_token":  token,
		"remove_extensions": []string{},
		"force":             true,
	}}, nil
}

// hostRecipeCreateProjectRemoval removes the fixture project. It is the
// host-wide stage's prepare for createProjectRemoval.
func hostRecipeCreateProjectRemoval(ctx context.Context, s *conformanceSweep, _ *conformanceOp) (successInputs, error) {
	if _, err := hostRecipeT(s); err != nil {
		return successInputs{}, err
	}
	return hostRecipeRemovalRequest(ctx, s, s.fixture.project.ID)
}

// hostRecipeGetProjectRemoval removes a project of its own once, lets the
// removal settle, and reads its outcome.
func hostRecipeGetProjectRemoval(ctx context.Context, s *conformanceSweep, _ *conformanceOp) (successInputs, error) {
	t, err := hostRecipeT(s)
	if err != nil {
		return successInputs{}, err
	}
	key, err := hostRecipeOnce(s, "removal", func() (string, error) {
		dir := s.host.ProjectDir(t, hostRecipeID("conformance-removal"))
		body, err := json.Marshal(map[string]any{"roots": []any{map[string]any{"path": dir}}})
		if err != nil {
			return "", err
		}
		created, err := s.prerequisite(ctx, "createProject", successInputs{body: body, contentType: "application/json"})
		if err != nil {
			return "", err
		}
		projectID, _ := created["id"].(string)
		in, err := hostRecipeRemovalRequest(ctx, s, projectID)
		if err != nil {
			return "", err
		}
		body, err = json.Marshal(in.fields)
		if err != nil {
			return "", err
		}
		if _, err := s.prerequisite(ctx, "createProjectRemoval", successInputs{
			params: in.params, body: body, contentType: "application/json",
		}); err != nil {
			return "", err
		}
		s.host.Server.WaitForBackground(ctx)
		return projectID + "\x00" + in.fields["operation_id"].(string), nil
	})
	if err != nil {
		return successInputs{}, err
	}
	projectID, operationID, _ := strings.Cut(key, "\x00")
	return successInputs{params: map[string]string{"id": projectID, "operation_id": operationID}}, nil
}

// hostRecipeRestoreBackup uploads the archive the host's own export writes.
// It is the host-wide stage's prepare for restoreBackup.
func hostRecipeRestoreBackup(ctx context.Context, s *conformanceSweep, _ *conformanceOp) (successInputs, error) {
	op := s.op("exportBackup")
	if op == nil {
		return successInputs{}, errors.New("no operation exportBackup")
	}
	params, _ := s.values.pathParams(op)
	ex := s.send(ctx, conformanceRequest{op: op, probe: "prerequisite", params: params, query: s.values.requiredQuery(op)})
	if ex.status != http.StatusOK {
		return successInputs{}, fmt.Errorf("exportBackup answered %s", ex.outcome())
	}
	return successInputs{body: ex.body, contentType: "application/zip"}, nil
}
