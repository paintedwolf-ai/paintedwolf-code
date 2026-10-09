package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/google/uuid"
	"github.com/lycaon/lycaon/internal/enginepaths"
	"github.com/lycaon/lycaon/internal/extpacks"
	"github.com/lycaon/lycaon/internal/localdata"
	sessionstore "github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/internal/testutil"
	wire "github.com/lycaon/lycaon/pkg/api"
)

func TestLocalDataGetAndClear(t *testing.T) {
	base := t.TempDir()
	t.Setenv("LYCAON_CONFIG_DIR", base)

	fetchDir := filepath.Join(base, "fetch-cache")
	testutil.FailErr(t, "mkdir fetch", os.MkdirAll(fetchDir, 0o700))
	testutil.FailErr(t, "write fetch", os.WriteFile(filepath.Join(fetchDir, "a"), []byte("body"), 0o600))
	testutil.FailErr(t, "seed durable", localdata.SeedDurableMarkers(base))

	srv := NewServer(requiredTestDeps(t, Dependencies{Store: sessionstore.NewMemory(), DataDir: base}), nil, TestAPIToken)

	req := httptest.NewRequest(http.MethodGet, "/v1/local-data", nil)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("unauthed GET status=%d", w.Code)
	}

	req = newAuthedRequest(http.MethodGet, "/v1/local-data", nil)
	w = httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("GET status=%d body=%s", w.Code, w.Body.String())
	}
	var status wire.LocalDataStatus
	testutil.FailErr(t, "decode", json.NewDecoder(w.Body).Decode(&status))
	if len(status.Buckets) != len(localdata.Catalog()) {
		t.Fatalf("buckets=%d want %d", len(status.Buckets), len(localdata.Catalog()))
	}
	var fetchPresent bool
	for _, b := range status.Buckets {
		if b.ID == wire.LocalDataBucketFetchCache {
			fetchPresent = b.Present
		}
	}
	if !fetchPresent {
		t.Fatal("fetch_cache should be present")
	}

	body, _ := json.Marshal(wire.LocalDataClearRequest{
		Buckets: []wire.LocalDataBucketId{wire.LocalDataBucketFetchCache},
	})
	req = newAuthedRequest(http.MethodPost, "/v1/local-data/clear", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w = httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("clear status=%d body=%s", w.Code, w.Body.String())
	}
	var clearResp wire.LocalDataClearResponse
	testutil.FailErr(t, "decode clear", json.NewDecoder(w.Body).Decode(&clearResp))
	if len(clearResp.Results) != 1 || !clearResp.Results[0].OK {
		t.Fatalf("clear results: %+v", clearResp.Results)
	}

	entries, err := os.ReadDir(fetchDir)
	testutil.FailErr(t, "readdir fetch", err)
	if len(entries) != 0 {
		t.Fatalf("fetch-cache not cleared: %v", entries)
	}
	for _, p := range localdata.DurableAbsPaths(base) {
		data, err := os.ReadFile(p)
		testutil.FailErr(t, "durable", err)
		if string(data) != "keep\n" {
			t.Fatalf("durable wiped: %s", p)
		}
	}
}

func TestExtensionCacheClearReplacesDerivedCatalog(t *testing.T) {
	base := t.TempDir()
	t.Setenv("LYCAON_CONFIG_DIR", base)
	cache := filepath.Join(base, enginepaths.ExtensionsCacheDirName)
	testutil.FailErr(t, "mkdir extension cache", os.MkdirAll(cache, 0o700))
	testutil.FailErr(t, "write extension body", os.WriteFile(filepath.Join(cache, "body"), []byte("cached"), 0o600))

	deps := Dependencies{Store: sessionstore.NewMemory(), DataDir: base}
	withExtensionOwner(t)(&deps)
	srv := NewServer(requiredTestDeps(t, deps), nil, TestAPIToken)
	extpacks.SetActive(&extpacks.EffectiveCatalog{Desired: extpacks.DesiredState{
		Disabled: []string{"sentinel/stale-catalog"},
	}})
	t.Cleanup(extpacks.ClearActive)

	body, err := json.Marshal(wire.LocalDataClearRequest{
		Buckets: []wire.LocalDataBucketId{wire.LocalDataBucketExtensionCache},
	})
	testutil.FailErr(t, "marshal clear", err)
	req := newAuthedRequest(http.MethodPost, "/v1/local-data/clear", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("clear status=%d body=%s", w.Code, w.Body.String())
	}
	active := extpacks.Active()
	if active == nil || len(active.Desired.Disabled) != 0 {
		t.Fatalf("cache clear left stale active catalog: %+v", active)
	}
	entries, readErr := os.ReadDir(cache)
	testutil.FailErr(t, "read cleared cache", readErr)
	if len(entries) != 0 {
		t.Fatalf("extension cache not cleared: %v", entries)
	}
}

// Without an extension owner there is no derived catalog to replace, and the
// extension cache still clears as plain files.
func TestExtensionCacheClearsWithoutOwner(t *testing.T) {
	base := t.TempDir()
	cache := filepath.Join(base, enginepaths.ExtensionsCacheDirName)
	testutil.FailErr(t, "mkdir extension cache", os.MkdirAll(cache, 0o700))
	testutil.FailErr(t, "write extension body", os.WriteFile(filepath.Join(cache, "body"), []byte("cached"), 0o600))

	srv := NewServer(requiredTestDeps(t, Dependencies{Store: sessionstore.NewMemory(), DataDir: base}), nil, TestAPIToken)
	reg, err := srv.localDataRegistry()
	testutil.FailErr(t, "local data registry", err)
	testutil.FailErr(t, "clear extension cache", reg.ClearOne(t.Context(), localdata.BucketExtensionCache))
	if _, err := os.Stat(filepath.Join(cache, "body")); !os.IsNotExist(err) {
		t.Fatalf("extension cache body survived the clear: %v", err)
	}
}

func TestLocalDataUnknownBucket400(t *testing.T) {
	base := t.TempDir()
	srv := NewServer(requiredTestDeps(t, Dependencies{Store: sessionstore.NewMemory(), DataDir: base}), nil, TestAPIToken)

	body := []byte(`{"buckets":["not_a_real_bucket"]}`)
	req := newAuthedRequest(http.MethodPost, "/v1/local-data/clear", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status=%d want 400 body=%s", w.Code, w.Body.String())
	}
}

func TestLocalDataEmptyBuckets400(t *testing.T) {
	base := t.TempDir()
	srv := NewServer(requiredTestDeps(t, Dependencies{Store: sessionstore.NewMemory(), DataDir: base}), nil, TestAPIToken)

	body := []byte(`{"buckets":[]}`)
	req := newAuthedRequest(http.MethodPost, "/v1/local-data/clear", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status=%d want 400 body=%s", w.Code, w.Body.String())
	}
}

func TestLocalDataInventoriesAndClearsOneWorkspaceCache(t *testing.T) {
	base := t.TempDir()
	seedRoot := enginepaths.WorkerSeedsRootUnder(base)
	sourceRoot := filepath.Join(t.TempDir(), "firefox")
	testutil.FailErr(t, "mkdir source", os.MkdirAll(sourceRoot, 0o755))
	id := enginepaths.ProjectKey(sourceRoot)
	generation := uuid.NewString()
	seedDir := filepath.Join(seedRoot, id)
	generationDir := filepath.Join(seedDir, "generations", generation)
	testutil.FailErr(t, "mkdir cache tree", os.MkdirAll(filepath.Join(generationDir, "tree"), 0o700))
	testutil.FailErr(t, "write cache file", os.WriteFile(filepath.Join(generationDir, "tree", "main.cpp"), []byte("code"), 0o600))
	manifest := `{"format":1,"source_root":` + string(mustJSON(t, sourceRoot)) + `,"entries":{"main.cpp":{"kind":"regular","size":4,"mode":420,"mod_time":0}}}`
	testutil.FailErr(t, "write cache manifest", os.WriteFile(filepath.Join(generationDir, "manifest.json"), []byte(manifest), 0o600))
	testutil.FailErr(t, "write cache current", os.WriteFile(filepath.Join(seedDir, "CURRENT"), []byte(generation+"\n"), 0o600))
	testutil.FailErr(t, "write cache last-used", os.WriteFile(filepath.Join(seedDir, "LAST_USED"), nil, 0o600))

	srv := NewServer(requiredTestDeps(t, Dependencies{Store: sessionstore.NewMemory(), DataDir: base, WorkerSeedRoot: seedRoot}), nil, TestAPIToken)
	req := newAuthedRequest(http.MethodGet, "/v1/local-data", nil)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("GET status=%d body=%s", w.Code, w.Body.String())
	}
	var status wire.LocalDataStatus
	testutil.FailErr(t, "decode workspace caches", json.NewDecoder(w.Body).Decode(&status))
	if len(status.WorkspaceCaches) != 1 || status.WorkspaceCaches[0].ID != id || status.WorkspaceCaches[0].LogicalBytes != 4 {
		t.Fatalf("workspace caches = %+v", status.WorkspaceCaches)
	}

	body, err := json.Marshal(wire.LocalDataClearRequest{WorkspaceCacheIDs: []string{id}})
	testutil.FailErr(t, "marshal cache clear", err)
	req = newAuthedRequest(http.MethodPost, "/v1/local-data/clear", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w = httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("clear status=%d body=%s", w.Code, w.Body.String())
	}
	var cleared wire.LocalDataClearResponse
	testutil.FailErr(t, "decode cache clear", json.NewDecoder(w.Body).Decode(&cleared))
	if len(cleared.WorkspaceCacheResults) != 1 || !cleared.WorkspaceCacheResults[0].OK {
		t.Fatalf("workspace cache clear = %+v", cleared.WorkspaceCacheResults)
	}
	if _, err := os.Stat(seedDir); !os.IsNotExist(err) {
		t.Fatalf("workspace cache remains after clear: %v", err)
	}
}

func mustJSON(t *testing.T, value string) []byte {
	t.Helper()
	raw, err := json.Marshal(value)
	testutil.FailErr(t, "marshal JSON string", err)
	return raw
}
