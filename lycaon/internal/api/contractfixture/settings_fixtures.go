package contractfixture

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	hostapi "github.com/lycaon/lycaon/internal/api"
	"github.com/lycaon/lycaon/internal/configdir"
	"github.com/lycaon/lycaon/internal/events"
	"github.com/lycaon/lycaon/internal/filebriefing"
	"github.com/lycaon/lycaon/internal/hitl"
	"github.com/lycaon/lycaon/internal/llm"
	"github.com/lycaon/lycaon/internal/project"
	"github.com/lycaon/lycaon/internal/session"
	"github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/internal/settings"
	"github.com/lycaon/lycaon/internal/testutil"
	wire "github.com/lycaon/lycaon/pkg/api"
)

func ApprovalsFixture(t *testing.T) (base, query string) {
	t.Helper()
	_, base, reg := NewSettingsTestServer(t)
	proj, err := project.CreateWithRoot(t.Context(), reg, t.TempDir())
	testutil.FailErr(t, "reg.Create failed", err)
	PutApprovals(t, base, "", `{"approval_posture":"balanced","ai_rationale_enabled":false}`, http.StatusOK)
	return base, "project_id=" + proj.ID
}

// putApprovals writes one approvals layer and asserts the expected status.

type FailingClearFileBriefingStore struct {
	filebriefing.Store
}

func GetApprovals(t *testing.T, base, query string) wire.ApprovalConfigResponse {
	t.Helper()
	url := base + "/v1/settings/approvals"
	if query != "" {
		url += "?" + query
	}
	resp, err := AuthedHTTPGet(url)
	testutil.FailErr(t, "get approvals", err)
	defer func() { _ = resp.Body.Close() }()
	var out wire.ApprovalConfigResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		testutil.FailErr(t, "decode approvals", err)
	}
	return out
}

func GetGlobalApprovalConfig(t *testing.T, base string) wire.ApprovalConfigResponse {
	t.Helper()
	resp, err := AuthedHTTPGet(base + "/v1/settings/approvals")
	testutil.FailErr(t, "get approvals", err)
	defer func() { _ = resp.Body.Close() }()
	var cfg wire.ApprovalConfigResponse
	if err := json.NewDecoder(resp.Body).Decode(&cfg); err != nil {
		testutil.FailErr(t, "decode approvals", err)
	}
	return cfg
}

func NewSettingsTestServer(t *testing.T, opts ...TestDeps) (*hostapi.Server, string, *project.MemoryRegistry) {
	t.Helper()
	tmp := t.TempDir()
	t.Setenv(configdir.EnvConfigDir, tmp)
	svc, err := settings.NewService()
	if err != nil {
		t.Fatalf("settings service: %v", err)
	}

	store := store.NewMemory()
	mock := llm.NewMockProvider(TestMockConfig(t))
	mgr := session.NewHost(store, session.Models{Client: mock, Provider: nil, Limits: settings.DefaultSessionLimits(), Cost: nil}, nil)
	mgr.SetLimitsProvider(settings.ProjectLimitsAdapter{Store: svc.Limits})
	project.SetDefaultOpenPolicy(project.TestOpenPolicy())
	reg := project.NewMemoryRegistry()
	hub := events.NewMemoryHub()
	deps := hostapi.Dependencies{Core: hostapi.CoreDependencies{Store: store, Projects: reg, Sessions: mgr, Settings: svc}, Host: hostapi.HostDependencies{Events: hub}}
	for _, opt := range opts {
		opt(&deps)
	}
	srv := hostapi.NewServer(RequiredTestDeps(t, deps), nil, hostapi.TestAPIToken)
	return srv, StartTestHTTPServer(t, srv), reg
}

func PutApprovals(t *testing.T, base, query, body string, want int) {
	t.Helper()
	url := base + "/v1/settings/approvals"
	if query != "" {
		url += "?" + query
	}
	req, err := http.NewRequestWithContext(t.Context(), http.MethodPatch,
		url, strings.NewReader(body))
	testutil.FailErr(t, "build approvals PUT", err)
	req.Header.Set("Content-Type", "application/json")
	hostapi.WithTestAuth(req)
	resp, err := fixtureClient.Do(req)
	testutil.FailErr(t, "approvals PUT", err)
	_ = resp.Body.Close()
	if resp.StatusCode != want {
		t.Fatalf("PUT %s %s status = %d, want %d", query, body, resp.StatusCode, want)
	}
}

func ReadSettingsBody(t *testing.T, resp *http.Response) string {
	t.Helper()
	body, err := io.ReadAll(resp.Body)
	testutil.FailErr(t, "io.ReadAll failed", err)
	return string(body)
}

func SeedToolAndWriteRootGrants(t *testing.T, srv *hostapi.Server, base string) (toolID, writeRootID string) {
	t.Helper()
	writeRoot := filepath.Join(t.TempDir(), "root")
	if err := os.MkdirAll(writeRoot, 0o750); err != nil {
		testutil.FailErr(t, "mkdir write root", err)
	}
	now := time.Now().UTC()
	makeGrant := func(category settings.ApprovalCategory, pattern, title string) settings.ApprovalGrant {
		witness := hitl.ApprovalGrantWitness{}
		return settings.ApprovalGrant{
			GrantedByPersonID: testutil.HostOwner().ID,
			ID:                hitl.ApprovalGrantID(hitl.ApprovalGrantScopeDevice, string(category), pattern, "", "", witness, nil),
			Scope:             hitl.ApprovalGrantScopeDevice, Category: category, Pattern: pattern,
			Title: title, Coverage: pattern, GrantedAt: now,
			ExpiresWhen: "when revoked", ReaskWhen: "the action changes", Witness: witness,
		}
	}
	if _, err := srv.Admin.Project.Trust.Settings.Approvals.UpsertGlobalGrant(makeGrant(settings.ApprovalCategoryTool, "read", "Allow read for this project")); err != nil {
		testutil.FailErr(t, "upsert tool lease", err)
	}
	if _, err := srv.Admin.Project.Trust.Settings.Approvals.UpsertGlobalGrant(makeGrant(settings.ApprovalCategoryWriteRoot, writeRoot, "Allow write root")); err != nil {
		testutil.FailErr(t, "upsert write_root", err)
	}
	listResp, err := AuthedHTTPGet(base + "/v1/approval-grants")
	testutil.FailErr(t, "list grants", err)
	defer func() { _ = listResp.Body.Close() }()
	if listResp.StatusCode != http.StatusOK {
		t.Fatalf("list grants status = %d body = %s", listResp.StatusCode, ReadSettingsBody(t, listResp))
	}
	var grants wire.ApprovalGrantsResponse
	if err := json.NewDecoder(listResp.Body).Decode(&grants); err != nil {
		testutil.FailErr(t, "decode grants", err)
	}
	if len(grants.Grants) != 2 {
		t.Fatalf("grants = %+v", grants.Grants)
	}
	for _, g := range grants.Grants {
		if g.Title == "" {
			t.Fatalf("empty title on %+v", g)
		}
		switch g.Category {
		case wire.ApprovalGrantCategoryTool:
			toolID = g.ID
		case wire.ApprovalGrantCategoryWriteRoot:
			writeRootID = g.ID
			if g.Unavailable {
				t.Fatalf("existing write_root should be available: %+v", g)
			}
		default:
		}
	}
	if toolID == "" || writeRootID == "" {
		t.Fatalf("missing grant ids: %+v", grants.Grants)
	}
	return toolID, writeRootID
}

func (FailingClearFileBriefingStore) Clear(context.Context) error { return errors.New("clear failed") }
