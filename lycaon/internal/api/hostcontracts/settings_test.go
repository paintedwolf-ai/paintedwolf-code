package hostcontracts

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	hostapi "github.com/lycaon/lycaon/internal/api"
	contractfixture "github.com/lycaon/lycaon/internal/api/contractfixture"
	"github.com/lycaon/lycaon/internal/project"
	"github.com/lycaon/lycaon/internal/settings"
	"github.com/lycaon/lycaon/internal/testutil"
	wire "github.com/lycaon/lycaon/pkg/api"
)

func TestPutPostureAndPutLimits(t *testing.T) {
	_, base, _ := contractfixture.NewSettingsTestServer(t)

	putPosture, _ := http.NewRequestWithContext(t.Context(), http.MethodPatch, base+"/v1/settings/approvals",
		strings.NewReader(`{"rules":[],"approval_posture":"strict"}`))
	putPosture.Header.Set("Content-Type", "application/json")
	hostapi.WithTestAuth(putPosture)
	postureResp, err := http.DefaultClient.Do(putPosture)
	testutil.FailErr(t, "http.DefaultClient.Do failed", err)
	postureResp.Body.Close()
	if postureResp.StatusCode != http.StatusOK {
		t.Fatalf("put posture status = %d", postureResp.StatusCode)
	}

	getResp, err := contractfixture.AuthedHTTPGet(base + "/v1/settings/approvals")
	testutil.FailErr(t, "authedHTTPGet failed", err)
	defer getResp.Body.Close()
	var cfg wire.ApprovalConfigResponse
	if err := json.NewDecoder(getResp.Body).Decode(&cfg); err != nil {
		testutil.FailErr(t, "json.NewDecoder failed", err)
	}
	if cfg.ApprovalPosture != "strict" {
		t.Fatalf("posture = %q, want strict", cfg.ApprovalPosture)
	}

	putLimits, _ := http.NewRequestWithContext(t.Context(), http.MethodPatch, base+"/v1/settings/limits", strings.NewReader(`{
		"max_iterations": 10,
		"overlay_promote_max_iterations": 30,
		"max_tool_result_bytes": 65536,
		"llm_turn_timeout_ms": 3600000,
		"coordinator_host_turn_timeout_ms": 3600000,
		"coordinator_max_sleep_ms": 3600000,
		"await_parent_workers_timeout_ms": 3600000
	}`))
	putLimits.Header.Set("Content-Type", "application/json")
	hostapi.WithTestAuth(putLimits)
	limitsResp, err := http.DefaultClient.Do(putLimits)
	testutil.FailErr(t, "http.DefaultClient.Do failed", err)
	defer limitsResp.Body.Close()
	if limitsResp.StatusCode != http.StatusOK {
		t.Fatalf("put limits status = %d body = %s", limitsResp.StatusCode, contractfixture.ReadSettingsBody(t, limitsResp))
	}
	var lim wire.SettingsLimitsResponse
	if err := json.NewDecoder(limitsResp.Body).Decode(&lim); err != nil {
		testutil.FailErr(t, "json.NewDecoder failed", err)
	}
	if lim.MaxIterations != 10 {
		t.Fatalf("max_iterations = %d", lim.MaxIterations)
	}
	if lim.SpendWarningRatio != 0.8 {
		t.Fatalf("spend_warning_ratio = %v want default 0.8", lim.SpendWarningRatio)
	}
	if !lim.SpendSoftStop {
		t.Fatal("spend soft stop should default on")
	}

	putImmediate, _ := http.NewRequestWithContext(t.Context(), http.MethodPatch, base+"/v1/settings/limits", strings.NewReader(`{
		"max_iterations": 10,
		"overlay_promote_max_iterations": 30,
		"max_tool_result_bytes": 65536,
		"llm_turn_timeout_ms": 3600000,
		"coordinator_host_turn_timeout_ms": 3600000,
		"coordinator_max_sleep_ms": 3600000,
		"await_parent_workers_timeout_ms": 3600000,
		"spend_soft_stop": false
	}`))
	putImmediate.Header.Set("Content-Type", "application/json")
	hostapi.WithTestAuth(putImmediate)
	immediateResp, err := http.DefaultClient.Do(putImmediate)
	testutil.FailErr(t, "put immediate spend closeout", err)
	defer immediateResp.Body.Close()
	if immediateResp.StatusCode != http.StatusOK {
		t.Fatalf("put immediate closeout status = %d body = %s", immediateResp.StatusCode, contractfixture.ReadSettingsBody(t, immediateResp))
	}
	if err := json.NewDecoder(immediateResp.Body).Decode(&lim); err != nil {
		testutil.FailErr(t, "decode immediate closeout limits", err)
	}
	if lim.SpendSoftStop {
		t.Fatal("explicit false should disable the spend soft stop")
	}
}

func TestPutApprovalsRejectsInvalidJSONWithoutMutation(t *testing.T) {
	_, base, _ := contractfixture.NewSettingsTestServer(t)
	before := contractfixture.GetGlobalApprovalConfig(t, base)

	for _, body := range []string{
		`{"approval_posture":"strict","unexpected":true}`,
		`{"approval_posture":"strict"} {}`,
	} {
		req, err := http.NewRequestWithContext(t.Context(), http.MethodPatch, base+"/v1/settings/approvals", strings.NewReader(body))
		testutil.FailErr(t, "create invalid approvals request", err)
		req.Header.Set("Content-Type", "application/json")
		hostapi.WithTestAuth(req)
		resp, err := http.DefaultClient.Do(req)
		testutil.FailErr(t, "send invalid approvals request", err)
		resp.Body.Close()
		if resp.StatusCode != http.StatusBadRequest {
			t.Fatalf("invalid approvals status = %d", resp.StatusCode)
		}
	}

	after := contractfixture.GetGlobalApprovalConfig(t, base)
	if after.ApprovalPosture != before.ApprovalPosture {
		t.Fatalf("approval posture changed after rejected payload: got %q want %q", after.ApprovalPosture, before.ApprovalPosture)
	}
}

func TestApprovalsRejectsUnknownProjectID(t *testing.T) {
	_, base, _ := contractfixture.NewSettingsTestServer(t)
	unknown := "00000000-0000-4000-8000-000000000099"
	resp, err := contractfixture.AuthedHTTPGet(base + "/v1/settings/approvals?project_id=" + unknown)
	testutil.FailErr(t, "authedHTTPGet failed", err)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("status = %d body = %s", resp.StatusCode, contractfixture.ReadSettingsBody(t, resp))
	}
	var errResp wire.ErrorResponse
	if err := json.NewDecoder(resp.Body).Decode(&errResp); err != nil {
		testutil.FailErr(t, "json.NewDecoder failed", err)
	}
	if errResp.Code != wire.ApiErrorCodeProjectNotFound {
		t.Fatalf("code = %q", errResp.Code)
	}
}

func TestProjectScopedApprovalsIsolated(t *testing.T) {
	_, base, reg := contractfixture.NewSettingsTestServer(t)
	dirA := t.TempDir()
	dirB := t.TempDir()
	projA, err := project.CreateWithRoot(t.Context(), reg, dirA)
	testutil.FailErr(t, "reg.Create failed", err)
	projB, err := project.CreateWithRoot(t.Context(), reg, dirB)
	testutil.FailErr(t, "reg.Create failed", err)

	const marker = "lycaon_security_isolated_marker"
	putReq, _ := http.NewRequestWithContext(t.Context(), http.MethodPatch, base+"/v1/settings/approvals?project_id="+projA.ID,
		strings.NewReader(`{"rules":[{"category":"tool","pattern":"`+marker+`","effect":"deny"}]}`))
	putReq.Header.Set("Content-Type", "application/json")
	hostapi.WithTestAuth(putReq)
	putResp, err := http.DefaultClient.Do(putReq)
	testutil.FailErr(t, "http.DefaultClient.Do failed", err)
	putResp.Body.Close()
	if putResp.StatusCode != http.StatusOK {
		t.Fatalf("put status = %d", putResp.StatusCode)
	}

	getA, err := contractfixture.AuthedHTTPGet(base + "/v1/settings/approvals?project_id=" + projA.ID)
	testutil.FailErr(t, "authedHTTPGet failed", err)
	defer getA.Body.Close()
	var cfgA wire.ApprovalConfigResponse
	if err := json.NewDecoder(getA.Body).Decode(&cfgA); err != nil {
		testutil.FailErr(t, "json.NewDecoder failed", err)
	}
	found := false
	for _, r := range cfgA.Rules {
		if r.Pattern == marker {
			found = true
		}
	}
	if !found {
		t.Fatal("expected project A overlay rule")
	}

	getOther, err := contractfixture.AuthedHTTPGet(base + "/v1/settings/approvals?project_id=" + projB.ID)
	testutil.FailErr(t, "authedHTTPGet failed", err)
	defer getOther.Body.Close()
	body := contractfixture.ReadSettingsBody(t, getOther)
	if strings.Contains(body, marker) {
		t.Fatalf("project B approvals leaked project A overlay: %s", body)
	}
}

// approvalsFixture stands up a project whose device layer is Balanced.

func TestProjectApprovalsInheritAndFieldSources(t *testing.T) {
	base, q := contractfixture.ApprovalsFixture(t)

	inherited := contractfixture.GetApprovals(t, base, q)
	if inherited.ApprovalPosture != "balanced" {
		t.Fatalf("inherited posture = %q", inherited.ApprovalPosture)
	}
	if inherited.FieldSources == nil || inherited.FieldSources.ApprovalPosture != "default" {
		t.Fatalf("field_sources = %+v", inherited.FieldSources)
	}
	if inherited.FieldSources.NeverAsk != "default" {
		t.Fatalf("field_sources.never_ask = %q", inherited.FieldSources.NeverAsk)
	}
	if inherited.Defaults == nil || inherited.Defaults.ApprovalPosture != "balanced" {
		t.Fatalf("defaults = %+v", inherited.Defaults)
	}
	if inherited.Defaults.AIRationaleEnabled {
		t.Fatal("defaults.ai_rationale_enabled want false")
	}
	if inherited.Defaults.NeverAsk {
		t.Fatal("defaults.never_ask want false")
	}

	contractfixture.PutApprovals(t, base, q, `{"approval_posture":"strict"}`, http.StatusOK)
	overridden := contractfixture.GetApprovals(t, base, q)
	if overridden.ApprovalPosture != "strict" {
		t.Fatalf("override posture = %q", overridden.ApprovalPosture)
	}
	if overridden.FieldSources == nil || overridden.FieldSources.ApprovalPosture != "override" {
		t.Fatalf("override field_sources = %+v", overridden.FieldSources)
	}

	// Clearing returns a field to the device layer: setting null in PATCH resets.
	contractfixture.PutApprovals(t, base, q, `{"approval_posture":null,"never_ask":null}`, http.StatusOK)
	cleared := contractfixture.GetApprovals(t, base, q)
	if cleared.ApprovalPosture != "balanced" {
		t.Fatalf("cleared posture = %q, want global balanced", cleared.ApprovalPosture)
	}
	if cleared.FieldSources == nil || cleared.FieldSources.ApprovalPosture != "default" {
		t.Fatalf("cleared field_sources = %+v", cleared.FieldSources)
	}
	if cleared.FieldSources.NeverAsk != "default" {
		t.Fatalf("cleared never_ask source = %q", cleared.FieldSources.NeverAsk)
	}
}

// A project layer is untrusted repo content: it may raise the ask-line for its own
// checkout and never lower it. Loosening is a client error, not a silent clamp.

func TestProjectApprovalsLayerMayOnlyTighten(t *testing.T) {
	base, q := contractfixture.ApprovalsFixture(t)

	contractfixture.PutApprovals(t, base, q, `{"approval_posture":"strict"}`, http.StatusOK)
	contractfixture.PutApprovals(t, base, q, `{"approval_posture":"light"}`, http.StatusBadRequest)
	// Project repo content can never disable approvals.
	contractfixture.PutApprovals(t, base, q, `{"never_ask":true}`, http.StatusBadRequest)

	// The tighter direction is useful: when device approvals are Off, a project may
	// restore asking for itself without changing the device setting.
	contractfixture.PutApprovals(t, base, "", `{"never_ask":true}`, http.StatusOK)
	contractfixture.PutApprovals(t, base, q, `{"never_ask":false}`, http.StatusOK)

	restored := contractfixture.GetApprovals(t, base, q)
	if restored.NeverAsk || restored.Defaults == nil || !restored.Defaults.NeverAsk ||
		restored.FieldSources == nil || restored.FieldSources.NeverAsk != "override" {
		t.Fatalf("restored approvals = %+v", restored)
	}
}

func TestVerifyProposalSurfacedThenSuppressed(t *testing.T) {
	t.Setenv("HOME", t.TempDir()) // isolate the persisted detect-proposal cache
	srv, base, reg := contractfixture.NewSettingsTestServer(t)
	dir := t.TempDir()
	proj, err := project.CreateWithRoot(t.Context(), reg, dir)
	testutil.FailErr(t, "CreateWithRoot failed", err)
	primary := project.PrimaryRootPath(proj)

	// A cached LLM proposal surfaces in GET while no test command is declared.
	srv.Admin.Project.Trust.Settings.Verify.SetProposal(primary, settings.VerifyDetectCandidate{Command: "./task check", Source: "README.md"})

	get := func() wire.VerifySettingsResponse {
		resp, err := contractfixture.AuthedHTTPGet(base + "/v1/settings/verify?project_id=" + proj.ID)
		testutil.FailErr(t, "GET verify failed", err)
		defer resp.Body.Close()
		var out wire.VerifySettingsResponse
		if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
			testutil.FailErr(t, "decode verify failed", err)
		}
		return out
	}

	if got := get(); got.DetectedCommand != "./task check" || got.DetectedSource != "README.md" {
		t.Fatalf("proposal not surfaced: %+v", got)
	}

	putReq, _ := http.NewRequestWithContext(t.Context(), http.MethodPatch,
		base+"/v1/settings/verify?project_id="+proj.ID,
		strings.NewReader(`{"test":"go test ./..."}`))
	putReq.Header.Set("Content-Type", "application/json")
	hostapi.WithTestAuth(putReq)
	putResp, err := http.DefaultClient.Do(putReq)
	testutil.FailErr(t, "PUT verify failed", err)
	putResp.Body.Close()
	if putResp.StatusCode != http.StatusOK {
		t.Fatalf("PUT verify status = %d", putResp.StatusCode)
	}

	// Declaring a test command suppresses the advisory banner.
	if got := get(); got.DetectedCommand != "" {
		t.Fatalf("proposal must be suppressed once a test is declared: %+v", got)
	}
}

func TestVerifyDismissEndpoint(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	srv, base, reg := contractfixture.NewSettingsTestServer(t)
	dir := t.TempDir()
	proj, err := project.CreateWithRoot(t.Context(), reg, dir)
	testutil.FailErr(t, "CreateWithRoot failed", err)
	primary := project.PrimaryRootPath(proj)
	srv.Admin.Project.Trust.Settings.Verify.SetProposal(primary, settings.VerifyDetectCandidate{Command: "./task check", Source: "README.md"})

	get := func() wire.VerifySettingsResponse {
		resp, err := contractfixture.AuthedHTTPGet(base + "/v1/settings/verify?project_id=" + proj.ID)
		testutil.FailErr(t, "GET verify failed", err)
		defer resp.Body.Close()
		var out wire.VerifySettingsResponse
		if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
			testutil.FailErr(t, "decode verify failed", err)
		}
		return out
	}
	dismiss := func(body string) wire.VerifySettingsResponse {
		req, _ := http.NewRequestWithContext(t.Context(), http.MethodPost,
			base+"/v1/settings/verify/dismiss?project_id="+proj.ID, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		hostapi.WithTestAuth(req)
		resp, err := http.DefaultClient.Do(req)
		testutil.FailErr(t, "POST dismiss failed", err)
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("dismiss status = %d body = %s", resp.StatusCode, contractfixture.ReadSettingsBody(t, resp))
		}
		var out wire.VerifySettingsResponse
		if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
			testutil.FailErr(t, "decode dismiss failed", err)
		}
		return out
	}

	if got := get(); got.SuggestionState != wire.VerifySuggestionStateSuggest {
		t.Fatalf("initial state = %q want suggest", got.SuggestionState)
	}
	if got := dismiss(`{"dismissed":true}`); got.SuggestionState != wire.VerifySuggestionStateDismissed || !got.Dismissed {
		t.Fatalf("after dismiss = %+v want dismissed", got)
	}
	if got := get(); got.SuggestionState != wire.VerifySuggestionStateDismissed {
		t.Fatalf("dismiss not persisted: %q", got.SuggestionState)
	}
	if got := dismiss(`{"dismissed":false}`); got.SuggestionState != wire.VerifySuggestionStateSuggest || got.Dismissed {
		t.Fatalf("after un-dismiss = %+v want suggest", got)
	}
}

func TestApprovalLeasesAreListedSeparatelyFromPolicy(t *testing.T) {
	srv, base, _ := contractfixture.NewSettingsTestServer(t)

	rawGet, err := contractfixture.AuthedHTTPGet(base + "/v1/settings/approvals")
	testutil.FailErr(t, "get approvals raw", err)
	body := contractfixture.ReadSettingsBody(t, rawGet)
	rawGet.Body.Close()
	if strings.Contains(body, `"grants"`) {
		t.Fatalf("policy GET must not emit reusable leases: %s", body)
	}
	_, _ = contractfixture.SeedToolAndWriteRootGrants(t, srv, base)
}

func TestApprovalGrantsBulkRevokeReportsPerID(t *testing.T) {
	srv, base, _ := contractfixture.NewSettingsTestServer(t)
	toolID, writeRootID := contractfixture.SeedToolAndWriteRootGrants(t, srv, base)

	resp, err := contractfixture.AuthedHTTPPost(
		base+"/v1/approval-grants/revoke",
		"application/json",
		`{"ids":["`+toolID+`","`+writeRootID+`","not-a-grant"]}`,
	)
	testutil.FailErr(t, "bulk revoke", err)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("bulk revoke status = %d body = %s", resp.StatusCode, contractfixture.ReadSettingsBody(t, resp))
	}
	var out wire.RevokeApprovalGrantsResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		testutil.FailErr(t, "decode bulk revoke", err)
	}
	if len(out.Results) != 3 {
		t.Fatalf("results = %+v want 3", out.Results)
	}
	byID := map[string]wire.ApprovalGrantRevokeResult{}
	for _, result := range out.Results {
		byID[result.ID] = result
	}
	if !byID[toolID].Revoked || !byID[writeRootID].Revoked {
		t.Fatalf("expected both seeded leases revoked: %+v", out.Results)
	}
	if byID["not-a-grant"].Revoked || byID["not-a-grant"].Code == "" {
		t.Fatalf("malformed id must fail with an error: %+v", byID["not-a-grant"])
	}

	missing, err := contractfixture.AuthedHTTPPost(
		base+"/v1/approval-grants/revoke",
		"application/json",
		`{"ids":["grant_does_not_exist"]}`,
	)
	testutil.FailErr(t, "revoke missing grant", err)
	defer missing.Body.Close()
	if missing.StatusCode != http.StatusOK {
		t.Fatalf("missing grant status = %d", missing.StatusCode)
	}
	var missingOut wire.RevokeApprovalGrantsResponse
	if err := json.NewDecoder(missing.Body).Decode(&missingOut); err != nil {
		testutil.FailErr(t, "decode missing revoke", err)
	}
	if len(missingOut.Results) != 1 || missingOut.Results[0].Revoked || missingOut.Results[0].Code == "" {
		t.Fatalf("nonexistent grant_* must not report revoked: %+v", missingOut.Results)
	}

	listResp, err := contractfixture.AuthedHTTPGet(base + "/v1/approval-grants")
	testutil.FailErr(t, "list after bulk revoke", err)
	defer listResp.Body.Close()
	var grants wire.ApprovalGrantsResponse
	if err := json.NewDecoder(listResp.Body).Decode(&grants); err != nil {
		testutil.FailErr(t, "decode after bulk revoke", err)
	}
	if len(grants.Grants) != 0 {
		t.Fatalf("grants after bulk revoke = %+v want none", grants.Grants)
	}
}

func TestApprovalGrantsPartialPutOmitsRulesAndRejectsUnknownEffect(t *testing.T) {
	srv, base, _ := contractfixture.NewSettingsTestServer(t)
	_, _ = contractfixture.SeedToolAndWriteRootGrants(t, srv, base)

	putPosture, _ := http.NewRequestWithContext(t.Context(), http.MethodPatch, base+"/v1/settings/approvals",
		strings.NewReader(`{"approval_posture":"strict"}`))
	putPosture.Header.Set("Content-Type", "application/json")
	hostapi.WithTestAuth(putPosture)
	postureResp, err := http.DefaultClient.Do(putPosture)
	testutil.FailErr(t, "put posture omit rules", err)
	postureResp.Body.Close()
	if postureResp.StatusCode != http.StatusOK {
		t.Fatalf("put posture status = %d", postureResp.StatusCode)
	}
	if got := contractfixture.GetGlobalApprovalConfig(t, base); got.ApprovalPosture != "strict" {
		t.Fatalf("posture = %q", got.ApprovalPosture)
	}
	listAfter, err := contractfixture.AuthedHTTPGet(base + "/v1/approval-grants")
	testutil.FailErr(t, "list grants after posture", err)
	var grantsAfter wire.ApprovalGrantsResponse
	if err := json.NewDecoder(listAfter.Body).Decode(&grantsAfter); err != nil {
		testutil.FailErr(t, "decode grants after posture", err)
	}
	listAfter.Body.Close()
	if len(grantsAfter.Grants) != 2 {
		t.Fatalf("omit-rules PUT wiped grants: %+v", grantsAfter.Grants)
	}

	putUnknown, _ := http.NewRequestWithContext(t.Context(), http.MethodPatch, base+"/v1/settings/approvals",
		strings.NewReader(`{"rules":[{"category":"tool","pattern":"read","effect":"permit"}]}`))
	putUnknown.Header.Set("Content-Type", "application/json")
	hostapi.WithTestAuth(putUnknown)
	unknownResp, err := http.DefaultClient.Do(putUnknown)
	testutil.FailErr(t, "put unknown effect", err)
	unknownBody := contractfixture.ReadSettingsBody(t, unknownResp)
	unknownResp.Body.Close()
	if unknownResp.StatusCode != http.StatusBadRequest {
		t.Fatalf("put unknown effect status = %d body = %s", unknownResp.StatusCode, unknownBody)
	}

	putDeny, _ := http.NewRequestWithContext(t.Context(), http.MethodPatch, base+"/v1/settings/approvals",
		strings.NewReader(`{"rules":[{"category":"host","pattern":"blocked.test","effect":"deny"}]}`))
	putDeny.Header.Set("Content-Type", "application/json")
	hostapi.WithTestAuth(putDeny)
	denyResp, err := http.DefaultClient.Do(putDeny)
	testutil.FailErr(t, "put deny rule", err)
	denyResp.Body.Close()
	if denyResp.StatusCode != http.StatusOK {
		t.Fatalf("put deny status = %d", denyResp.StatusCode)
	}
	cfgAfterDeny := contractfixture.GetGlobalApprovalConfig(t, base)
	foundDeny := false
	for _, r := range cfgAfterDeny.Rules {
		if r.Pattern == "blocked.test" && r.Effect == wire.ApprovalEffectDeny {
			foundDeny = true
		}
	}
	if !foundDeny {
		t.Fatalf("deny-only PUT did not persist policy: %+v", cfgAfterDeny.Rules)
	}
	listAfterDeny, err := contractfixture.AuthedHTTPGet(base + "/v1/approval-grants")
	testutil.FailErr(t, "list grants after deny", err)
	defer listAfterDeny.Body.Close()
	var grantsAfterDeny wire.ApprovalGrantsResponse
	if err := json.NewDecoder(listAfterDeny.Body).Decode(&grantsAfterDeny); err != nil {
		testutil.FailErr(t, "decode grants after deny", err)
	}
	if len(grantsAfterDeny.Grants) != 2 {
		t.Fatalf("policy PUT wiped leases: %+v", grantsAfterDeny.Grants)
	}
}

func TestApprovalGrantsRevokeOneLeavesOthers(t *testing.T) {
	srv, base, _ := contractfixture.NewSettingsTestServer(t)
	toolID, writeRootID := contractfixture.SeedToolAndWriteRootGrants(t, srv, base)

	resp, err := contractfixture.AuthedHTTPPost(
		base+"/v1/approval-grants/revoke",
		"application/json",
		`{"ids":["`+toolID+`"]}`,
	)
	testutil.FailErr(t, "revoke one", err)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("revoke status = %d body = %s", resp.StatusCode, contractfixture.ReadSettingsBody(t, resp))
	}
	var out wire.RevokeApprovalGrantsResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		testutil.FailErr(t, "decode revoke", err)
	}
	if len(out.Results) != 1 || !out.Results[0].Revoked {
		t.Fatalf("revoke results = %+v", out.Results)
	}

	listFinal, err := contractfixture.AuthedHTTPGet(base + "/v1/approval-grants")
	testutil.FailErr(t, "list grants final", err)
	var grantsFinal wire.ApprovalGrantsResponse
	if err := json.NewDecoder(listFinal.Body).Decode(&grantsFinal); err != nil {
		testutil.FailErr(t, "decode grants final", err)
	}
	listFinal.Body.Close()
	if len(grantsFinal.Grants) != 1 || grantsFinal.Grants[0].ID != writeRootID {
		t.Fatalf("after revoke grants = %+v", grantsFinal.Grants)
	}
}
