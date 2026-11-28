// Package wea_test implements TB09 · Secret screening and custody (A10, A11, A13)
// in-process lane tests for the Painted Wolf Code WEA bug bash.
//
// Strict D6/D7 discipline:
//   - Only synthetic, run-scoped test-local canaries are used.
//   - Real developer vaults, keychains, and credentials are never accessed.
//   - Leak assertions report only truncated SHA-256 digests, never raw canary bytes.
package wea_test

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/lycaon/lycaon/internal/configdir"
	"github.com/lycaon/lycaon/internal/credentialstore"
	"github.com/lycaon/lycaon/internal/credentialstore/credentialstoretest"
	"github.com/lycaon/lycaon/internal/db"
	"github.com/lycaon/lycaon/internal/mcp"
	"github.com/lycaon/lycaon/internal/mcp/mcptest"
	"github.com/lycaon/lycaon/internal/presence"
	"github.com/lycaon/lycaon/internal/secretcap"
	"github.com/lycaon/lycaon/internal/secretharvest"
	"github.com/lycaon/lycaon/internal/secretmatch"
	"github.com/lycaon/lycaon/internal/toolrejection"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/toolsecrets"
)

func tb09CanaryDigest(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:8])
}

func tb09MintCanary(label string) string {
	var buf [12]byte
	if _, err := rand.Read(buf[:]); err != nil {
		panic(err)
	}
	return fmt.Sprintf("WEA_CANARY_TB09_%s_%s", label, hex.EncodeToString(buf[:]))
}

func tb09ValidUUID(id string) bool {
	trimmed := strings.TrimSpace(id)
	parsed, err := uuid.Parse(trimmed)
	return err == nil && parsed.String() == trimmed
}

func tb09OpenStoreAndVault(t *testing.T, dbPath, vaultPath string) (*db.Store, *credentialstore.Store, string) {
	t.Helper()
	// D6 strict isolation: never touch the host developer config directory or vault.
	t.Setenv(configdir.EnvConfigDir, filepath.Join(filepath.Dir(vaultPath), "isolated-config"))
	store, err := db.Open(dbPath)
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	var ownerID string
	if err := store.QueryRowContext(t.Context(), `SELECT id FROM people WHERE role = 'owner' LIMIT 1`).Scan(&ownerID); err != nil {
		t.Fatalf("read owner person: %v", err)
	}
	values := credentialstoretest.Open(t, credentialstore.Slot{
		Path:      vaultPath,
		Namespace: credentialstore.NamespaceManagedSecrets,
		Context:   "wea tb09 synthetic test vault",
	}, tb09ValidUUID)
	return store, values, ownerID
}

func tb09SeedProjectAndSession(t *testing.T, store *db.Store, ownerID, projectID, sessionID string) {
	t.Helper()
	now := "2026-11-27T20:00:00Z"
	if _, err := store.ExecContext(t.Context(),
		`INSERT OR IGNORE INTO projects (id, name, last_opened_at, created_at) VALUES (?, ?, ?, ?)`,
		projectID, "WEA "+projectID, now, now,
	); err != nil {
		t.Fatalf("seed project %s: %v", projectID, err)
	}
	if sessionID != "" {
		if _, err := store.ExecContext(t.Context(),
			`INSERT OR IGNORE INTO sessions (id, project_id, owner_person_id, root_session_id, mode, posture, created_at, updated_at, activity_at)
			 VALUES (?, ?, ?, ?, 'agent', 'build', ?, ?, ?)`,
			sessionID, projectID, ownerID, sessionID, now, now, now,
		); err != nil {
			t.Fatalf("seed session %s: %v", sessionID, err)
		}
	}
}

// tb09ScanSQLiteRO scans every user table in dbPath read-only and fails with a
// D7-redacted digest (never raw canary bytes) if any canary appears anywhere in SQLite.
func tb09ScanSQLiteRO(t *testing.T, dbPath string, canaries []string) {
	t.Helper()
	ro, err := db.OpenReadOnly(t.Context(), dbPath)
	if err != nil {
		t.Fatalf("db.OpenReadOnly: %v", err)
	}
	defer ro.Close()

	rows, err := ro.QueryContext(t.Context(),
		`SELECT name FROM sqlite_master WHERE type = 'table' AND name NOT LIKE 'sqlite_%' ORDER BY name`)
	if err != nil {
		t.Fatalf("list sqlite tables: %v", err)
	}
	var tables []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			t.Fatalf("scan table name: %v", err)
		}
		tables = append(tables, name)
	}
	_ = rows.Close()

	for _, table := range tables {
		quoted := `"` + strings.ReplaceAll(table, `"`, `""`) + `"`
		trows, err := ro.QueryContext(t.Context(), "SELECT * FROM "+quoted)
		if err != nil {
			continue
		}
		cols, _ := trows.Columns()
		vals := make([]any, len(cols))
		ptrs := make([]any, len(cols))
		for i := range vals {
			ptrs[i] = &vals[i]
		}
		rowIdx := 0
		for trows.Next() {
			if err := trows.Scan(ptrs...); err != nil {
				break
			}
			for colIdx, v := range vals {
				if v == nil {
					continue
				}
				var text string
				switch x := v.(type) {
				case []byte:
					text = string(x)
				case string:
					text = x
				default:
					text = fmt.Sprint(x)
				}
				for _, c := range canaries {
					if c != "" && strings.Contains(text, c) {
						t.Errorf("D6/A11 canary leak detected in sqlite table=%s row=%d col=%s digest=sha256:%s",
							table, rowIdx, cols[colIdx], tb09CanaryDigest(c))
					}
				}
			}
			rowIdx++
		}
		_ = trows.Close()
	}
}

func tb09ToHarvested(fp *secretmatch.Fingerprinter, items []secretmatch.Remembered) []secretmatch.HarvestedValue {
	out := make([]secretmatch.HarvestedValue, 0, len(items))
	for _, it := range items {
		out = append(out, secretmatch.HarvestedValue{
			Name:           it.Name,
			Container:      it.Origin,
			Secret:         it.Secret,
			Fingerprint:    fp.Fingerprint(it.Secret),
			RuleID:         it.RuleID,
			Title:          it.Title,
			Source:         it.Source,
			Reference:      it.Reference,
			Retired:        it.Retired,
			NonDisclosable: it.NonDisclosable,
		})
	}
	return out
}

func tb09BuildMatcher(t *testing.T, harvested ...secretmatch.HarvestedValue) (*secretmatch.Matcher, *secretmatch.Fingerprinter) {
	t.Helper()
	m, err := secretmatch.BuildMatcher(secretmatch.Bundled())
	if err != nil {
		t.Fatalf("secretmatch.BuildMatcher: %v", err)
	}
	fp, err := secretmatch.NewFingerprinter(bytes.Repeat([]byte{0x5a}, 32))
	if err != nil {
		t.Fatalf("secretmatch.NewFingerprinter: %v", err)
	}
	m.SetFingerprinter(fp)
	if len(harvested) > 0 {
		m.SetHarvestSource(func(context.Context) []secretmatch.HarvestedValue {
			return harvested
		})
	}
	return m, fp
}

// TestWEATB09_A10_OutboundSecretScreeningOptions exercises outbound secret screening
// across mediated seams (MCP runtime, HTTP fixture endpoint, model request projection,
// and SecretReviewPayload metadata) and verifies every offered option:
//   - Withhold / Unanswered -> OUTBOUND_SECRET_DENIED, 0 bytes to endpoint, no canary in error
//   - SendRedacted          -> endpoint receives redacted placeholder, 0 canary bytes on wire
//   - SendUnchanged         -> endpoint receives verbatim canary only when explicitly approved
//   - TrackAndReplace       -> model_request vaults value into secretcap and projects reference token
func TestWEATB09_A10_OutboundSecretScreeningOptions(t *testing.T) {
	ctx := t.Context()
	tmp := t.TempDir()
	dbPath := filepath.Join(tmp, "tb09-a10.db")
	vaultPath := filepath.Join(tmp, credentialstore.VaultBasename)
	store, values, ownerID := tb09OpenStoreAndVault(t, dbPath, vaultPath)
	tb09SeedProjectAndSession(t, store, ownerID, "proj-a10", "chat-a10")

	// Synthetic AWS-shaped test-local token (triggers bundled catalog) + synthetic WEA canary (exact evidence).
	const syntheticAWSCanary = "AKIAQYJK5TXV4NZR7SGB"
	exactCanary := tb09MintCanary("a10_exact")

	// 1. Mediated MCP seam: Withhold, SendRedacted, SendUnchanged.
	mcpCfgPath := filepath.Join(tmp, "mcp.yaml")
	if err := os.WriteFile(mcpCfgPath, []byte(`providers:
  - id: tb09-fixture
    url: http://127.0.0.1:18765/mcp
    enabled: true
`), 0o600); err != nil {
		t.Fatalf("write mcp config: %v", err)
	}

	calls := map[string]map[string]int{}
	conn := &mcptest.Connector{
		Tools: map[string][]*sdkmcp.Tool{
			"tb09-fixture": {{Name: "deliver", Description: "deliver synthetic payload"}},
		},
		Results: map[string]map[string]string{
			"tb09-fixture": {"deliver": `{"ok":true}`},
		},
		Calls:    calls,
		CallArgs: map[string]map[string]map[string]any{},
	}
	reg, err := mcp.NewRuntime(mcp.RuntimeOptions{
		GlobalOverridePath: mcpCfgPath,
		Connector:          conn,
	})
	if err != nil {
		t.Fatalf("mcp.NewRuntime: %v", err)
	}
	reg.Tools.SetToolRegistry(tools.NewDefaultRegistry())

	matcher, fp := tb09BuildMatcher(t)
	matcher.SetHarvestSource(func(context.Context) []secretmatch.HarvestedValue {
		return []secretmatch.HarvestedValue{{
			Secret:      exactCanary,
			Fingerprint: fp.Fingerprint(exactCanary),
			Name:        "TB09_EXACT_CANARY",
			Container:   ".env",
		}}
	})
	if err := reg.Catalog.Load(ctx); err != nil {
		t.Fatalf("mcp catalog load: %v", err)
	}

	// 1a. Option: Withhold -> OUTBOUND_SECRET_DENIED, 0 endpoint calls, card names destination & kind.
	var lastAlert secretmatch.Alert
	reg.Calls.SetSecretScreen(matcher, func(_ context.Context, alert secretmatch.Alert) (secretmatch.Resolution, error) {
		lastAlert = alert
		return secretmatch.Resolution{Decision: secretmatch.Withhold}, nil
	})
	_, err = reg.Calls.CallTool(ctx, mcp.CallScope{}, "tb09-fixture", "deliver", map[string]any{
		"payload": "token=" + syntheticAWSCanary,
	})
	rej := toolrejection.AsToolReject(err)
	if rej == nil || rej.Code != "OUTBOUND_SECRET_DENIED" {
		t.Fatalf("withhold error = %v, want OUTBOUND_SECRET_DENIED", err)
	}
	if calls["tb09-fixture"]["deliver"] != 0 {
		t.Fatalf("withhold still invoked MCP endpoint: calls=%d", calls["tb09-fixture"]["deliver"])
	}
	if strings.Contains(rej.Error(), syntheticAWSCanary) {
		t.Fatalf("OUTBOUND_SECRET_DENIED error leaked canary digest=sha256:%s", tb09CanaryDigest(syntheticAWSCanary))
	}
	reviewCard := toolsecrets.SecretReviewPayload(lastAlert, nil, false)
	if reviewCard.DestinationLabel != "tb09-fixture" || reviewCard.DestinationKind != secretmatch.DestinationService || !reviewCard.CanRedact || reviewCard.CanTrack {
		t.Fatalf("unexpected MCP review card metadata: %+v", reviewCard)
	}

	// 1b. Option: SendRedacted -> endpoint receives redacted payload, canary bytes absent.
	reg.Calls.SetSecretScreen(matcher, func(_ context.Context, _ secretmatch.Alert) (secretmatch.Resolution, error) {
		return secretmatch.Resolution{Decision: secretmatch.SendRedacted}, nil
	})
	_, err = reg.Calls.CallTool(ctx, mcp.CallScope{}, "tb09-fixture", "deliver", map[string]any{
		"payload": "exact=" + exactCanary + " aws=" + syntheticAWSCanary,
	})
	if err != nil {
		t.Fatalf("MCP SendRedacted failed: %v", err)
	}
	sentRedacted, _ := conn.CallArgs["tb09-fixture"]["deliver"]["payload"].(string)
	if strings.Contains(sentRedacted, exactCanary) || strings.Contains(sentRedacted, syntheticAWSCanary) {
		t.Fatalf("MCP SendRedacted leaked canary bytes to endpoint")
	}
	if !strings.Contains(sentRedacted, "[REDACTED]") {
		t.Fatalf("MCP SendRedacted did not write redaction marker: %q", sentRedacted)
	}

	// 1c. Option: SendUnchanged -> endpoint receives verbatim canary only when explicitly approved.
	reg.Calls.SetSecretScreen(matcher, func(_ context.Context, _ secretmatch.Alert) (secretmatch.Resolution, error) {
		return secretmatch.Resolution{Decision: secretmatch.SendUnchanged}, nil
	})
	_, err = reg.Calls.CallTool(ctx, mcp.CallScope{}, "tb09-fixture", "deliver", map[string]any{
		"payload": "approved=" + exactCanary,
	})
	if err != nil {
		t.Fatalf("MCP SendUnchanged failed: %v", err)
	}
	sentApproved, _ := conn.CallArgs["tb09-fixture"]["deliver"]["payload"].(string)
	if sentApproved != "approved="+exactCanary {
		t.Fatalf("MCP SendUnchanged did not deliver approved payload verbatim")
	}

	// 2. Recording HTTP fixture endpoint + screen decisions (Withhold, SendRedacted, SendUnchanged).
	var mu sync.Mutex
	var recordedBodies []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		mu.Lock()
		recordedBodies = append(recordedBodies, string(b))
		mu.Unlock()
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer srv.Close()

	sendScreenedHTTP := func(payload string, decision secretmatch.Decision) error {
		hits := matcher.ScreenContext(ctx, payload)
		if len(hits) == 0 {
			return errors.New("expected secret screen match on synthetic canary")
		}
		if decision.Blocks() {
			return &toolrejection.ToolReject{Code: "OUTBOUND_SECRET_DENIED"}
		}
		wireBody := payload
		if decision == secretmatch.SendRedacted {
			wireBody = matcher.RedactString(ctx, payload)
		}
		resp, err := srv.Client().Post(srv.URL+"/ingest", "text/plain", strings.NewReader(wireBody))
		if err != nil {
			return err
		}
		_ = resp.Body.Close()
		return nil
	}

	if err := sendScreenedHTTP("secret="+exactCanary, secretmatch.Withhold); toolrejection.AsToolReject(err) == nil {
		t.Fatalf("HTTP Withhold did not return OUTBOUND_SECRET_DENIED")
	}
	if err := sendScreenedHTTP("secret="+exactCanary, secretmatch.SendRedacted); err != nil {
		t.Fatalf("HTTP SendRedacted failed: %v", err)
	}
	if err := sendScreenedHTTP("secret="+exactCanary, secretmatch.SendUnchanged); err != nil {
		t.Fatalf("HTTP SendUnchanged failed: %v", err)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(recordedBodies) != 2 {
		t.Fatalf("HTTP fixture received %d requests, want 2 (redacted + approved; withheld must not send)", len(recordedBodies))
	}
	if strings.Contains(recordedBodies[0], exactCanary) || !strings.Contains(recordedBodies[0], "[REDACTED]") {
		t.Fatalf("HTTP SendRedacted body did not strip canary")
	}
	if recordedBodies[1] != "secret="+exactCanary {
		t.Fatalf("HTTP SendUnchanged body did not match approved payload")
	}

	// 3. Model-request Protect (TrackAndReplace) vaults the canary and rewrites to a reference token.
	svc := secretcap.NewWithStore(store, values, nil)
	svc.SetFingerprinter(fp)
	modelAlert := secretmatch.Alert{
		Surface:          secretmatch.SurfaceModel,
		DestinationID:    "provider:synthetic",
		DestinationLabel: "Synthetic Model Provider",
		RuleID:           "gitleaks:aws-access-token",
	}
	modelCard := toolsecrets.SecretReviewPayload(modelAlert, nil, false)
	if modelCard.DestinationKind != secretmatch.DestinationModelProvider || !modelCard.CanTrack || !modelCard.CanRedact {
		t.Fatalf("model review card metadata mismatch: %+v", modelCard)
	}
	putRes, err := svc.Put(ctx, secretcap.PutRequest{
		ProjectID:     "proj-a10",
		ChatSessionID: "chat-a10",
		SessionID:     "chat-a10",
		OperationID:   "protect-a10-1",
		Name:          "Protected detected canary",
		Scope:         secretcap.ScopeChat,
		Origin:        secretcap.OriginDetected,
		Value:         exactCanary,
	})
	if err != nil {
		t.Fatalf("secretcap.Put (OriginDetected): %v", err)
	}
	screenVals, err := svc.ScreeningValues(ctx, "proj-a10", "chat-a10")
	if err != nil {
		t.Fatalf("ScreeningValues: %v", err)
	}
	matcher.SetHarvestSource(func(context.Context) []secretmatch.HarvestedValue {
		return tb09ToHarvested(fp, screenVals)
	})
	projected, reps := matcher.ProjectLabeledWhere(ctx, "prompt", "use key "+exactCanary+" now", nil)
	if strings.Contains(projected, exactCanary) {
		t.Fatalf("projected model payload still contains raw canary digest=sha256:%s", tb09CanaryDigest(exactCanary))
	}
	if !strings.Contains(projected, putRes.Metadata.Reference) || len(reps) != 1 || reps[0].Reference != putRes.Metadata.Reference {
		t.Fatalf("projected model payload did not substitute managed reference: projected=%q reps=%+v", projected, reps)
	}

	// 4. Verify zero canary bytes exist anywhere in the persisted SQLite database.
	tb09ScanSQLiteRO(t, dbPath, []string{exactCanary, syntheticAWSCanary})
}

// TestWEATB09_A11_ReferenceHandlingAndProjectScoping verifies:
//   - A valid reference token is never falsely reported as a raw secret by the matcher.
//   - A damaged/malformed reference is refused cleanly before resolution.
//   - Values shorter than MinManagedSecretRunes are never minted.
//   - References placed outside declared slots are detected (SECRET_REFERENCE_OUTSIDE_SINK invariant).
//   - One project's managed secrets and credential-file evidence are unreadable from another project.
func TestWEATB09_A11_ReferenceHandlingAndProjectScoping(t *testing.T) {
	ctx := t.Context()
	tmp := t.TempDir()
	dbPath := filepath.Join(tmp, "tb09-a11.db")
	vaultPath := filepath.Join(tmp, credentialstore.VaultBasename)
	store, values, ownerID := tb09OpenStoreAndVault(t, dbPath, vaultPath)

	tb09SeedProjectAndSession(t, store, ownerID, "proj-alpha", "chat-alpha")
	tb09SeedProjectAndSession(t, store, ownerID, "proj-beta", "chat-beta")

	matcher, fp := tb09BuildMatcher(t)
	svc := secretcap.NewWithStore(store, values, nil)
	svc.SetFingerprinter(fp)

	alphaCanary := tb09MintCanary("a11_alpha")
	alphaSecret, err := svc.CreateSettingsSecret(ctx, secretcap.CreateSettingsSecretRequest{
		ProjectID:   "proj-alpha",
		OperationID: "a11-alpha-1",
		PersonID:    ownerID,
		Name:        "Alpha Deploy Key",
		Purpose:     "deploy project alpha",
		Value:       alphaCanary,
	})
	if err != nil {
		t.Fatalf("CreateSettingsSecret(proj-alpha): %v", err)
	}

	// 1. Valid reference token is NEVER falsely reported as a raw secret.
	alphaEvidence, err := svc.ScreeningValues(ctx, "proj-alpha", "chat-alpha")
	if err != nil {
		t.Fatalf("ScreeningValues(proj-alpha): %v", err)
	}
	matcher.SetHarvestSource(func(context.Context) []secretmatch.HarvestedValue {
		return tb09ToHarvested(fp, alphaEvidence)
	})

	refBearingText := "STRIPE_SECRET_KEY=" + alphaSecret.Reference + "\nAWS_ACCESS_KEY_ID=" + alphaSecret.Reference
	if hits := matcher.ScreenContext(ctx, refBearingText); len(hits) != 0 {
		t.Fatalf("valid reference token falsely reported as secret: %+v", hits)
	}
	// Adjacent real canary beside a valid reference IS still caught without corrupting the reference token.
	mixedText := "REF=" + alphaSecret.Reference + " RAW=" + alphaCanary
	redactedMixed := matcher.RedactString(ctx, mixedText)
	if !strings.Contains(redactedMixed, alphaSecret.Reference) {
		t.Fatalf("redaction corrupted valid reference token: %q", redactedMixed)
	}
	if strings.Contains(redactedMixed, alphaCanary) {
		t.Fatalf("redaction missed adjacent raw canary digest=sha256:%s", tb09CanaryDigest(alphaCanary))
	}

	// 2. Damaged / malformed references are refused cleanly.
	malformedRefs := []string{
		"{{paintedwolf-secret:not-a-uuid}}",
		"{{paintedwolf-secret:11111111-2222-3333-4444-55555555555}}", // truncated
		"{{paintedwolf-secret:11111111-2222-3333-4444-555555555555",  // missing close
		"{{paintedwolf-secret:AAAAAAAA-BBBB-CCCC-DDDD-EEEEEEEEEEEE}}", // uppercase non-canonical
	}
	for _, bad := range malformedRefs {
		if _, err := secretcap.ParseReference(bad); !errors.Is(err, secretcap.ErrInvalidReference) {
			t.Fatalf("ParseReference(%q) = %v, want ErrInvalidReference", bad, err)
		}
		if !secretmatch.ContainsMalformedReferenceToken(bad) {
			t.Fatalf("ContainsMalformedReferenceToken(%q) = false, want true", bad)
		}
		use := secretcap.ReferenceUseInSlots(map[string]any{"body": bad}, []string{"body"})
		if !use.Malformed || use.Complete {
			t.Fatalf("ReferenceUseInSlots(%q) = %+v, want Malformed=true Complete=false", bad, use)
		}
	}

	// 3. Outside-sink slot detection (`SECRET_REFERENCE_OUTSIDE_SINK` invariant).
	outsideUse := secretcap.ReferenceUseOutsideSlots(
		map[string]any{"url": "https://example.invalid/?k=" + alphaSecret.Reference, "headers": []any{}},
		[]string{"headers", "body"},
	)
	if !outsideUse.Complete {
		t.Fatalf("ReferenceUseOutsideSlots failed to detect reference placed in url slot")
	}

	// 4. Values shorter than MinManagedSecretRunes are refused.
	for _, shortVal := range []string{"", "ab", strings.Repeat("x", secretmatch.MinManagedSecretRunes-1), strings.Repeat("é", secretmatch.MinManagedSecretRunes-1)} {
		if _, err := svc.CreateSettingsSecret(ctx, secretcap.CreateSettingsSecretRequest{
			ProjectID:   "proj-alpha",
			OperationID: "short-" + shortVal,
			PersonID:    ownerID,
			Name:        "Too short",
			Purpose:     "sub-floor check",
			Value:       shortVal,
		}); !errors.Is(err, secretcap.ErrValueTooShort) {
			t.Fatalf("short value %q error = %v, want ErrValueTooShort", shortVal, err)
		}
	}

	// 5. Project scoping: proj-beta CANNOT resolve, describe, list, or screen proj-alpha's secret.
	if _, err := svc.Resolve(ctx, map[string]any{"header": alphaSecret.Reference}, secretcap.ResolveContext{
		ProjectID:     "proj-beta",
		ChatSessionID: "chat-beta",
	}); !errors.Is(err, secretcap.ErrNotVisible) {
		t.Fatalf("cross-project Resolve error = %v, want ErrNotVisible", err)
	}
	if _, err := svc.Describe(ctx, "proj-beta", "chat-beta", alphaSecret.Reference); !errors.Is(err, secretcap.ErrNotVisible) {
		t.Fatalf("cross-project Describe error = %v, want ErrNotVisible", err)
	}
	betaListed, err := svc.ListProject(ctx, "proj-beta")
	if err != nil {
		t.Fatalf("ListProject(proj-beta): %v", err)
	}
	if len(betaListed) != 0 {
		t.Fatalf("proj-beta listed %d secrets from proj-alpha", len(betaListed))
	}
	betaEvidence, err := svc.ScreeningValues(ctx, "proj-beta", "chat-beta")
	if err != nil {
		t.Fatalf("ScreeningValues(proj-beta): %v", err)
	}
	if len(betaEvidence) != 0 {
		t.Fatalf("proj-beta received %d screening entries from proj-alpha", len(betaEvidence))
	}

	// 6. Harvested credential-file evidence is also strictly project-scoped.
	harvest := secretharvest.NewRuntime(fp)
	fileCanary := tb09MintCanary("a11_file_alpha")
	capAlpha := harvest.CaptureFiles("proj-alpha")
	if err := capAlpha.Observe(secretharvest.FileContent{
		ProjectID: "proj-alpha",
		RootID:    "root-alpha",
		Path:      ".env",
		Content:   []byte("ALPHA_DB_TOKEN=" + fileCanary + "\n"),
	}); err != nil {
		t.Fatalf("harvest Observe(proj-alpha): %v", err)
	}
	if err := capAlpha.Commit(); err != nil {
		t.Fatalf("harvest Commit(proj-alpha): %v", err)
	}
	if gotAlpha := harvest.FileValues("proj-alpha"); len(gotAlpha) != 1 || gotAlpha[0].Secret() != fileCanary {
		t.Fatalf("proj-alpha did not retain its own harvested file evidence: len=%d", len(gotAlpha))
	}
	if gotBeta := harvest.FileValues("proj-beta"); len(gotBeta) != 0 {
		t.Fatalf("proj-beta read %d file-evidence entries from proj-alpha", len(gotBeta))
	}
	if gotBetaAll := harvest.ValuesForProject("proj-beta"); len(gotBetaAll) != 0 {
		t.Fatalf("ValuesForProject(proj-beta) leaked %d entries from proj-alpha", len(gotBetaAll))
	}

	tb09ScanSQLiteRO(t, dbPath, []string{alphaCanary, fileCanary})
}

// TestWEATB09_A13_PresenceUnlockRevealReplaceRevokeRestart exercises:
//   - Presence unlock gating for person-held values (locked -> refused, unlocked + released -> allowed).
//   - Presence-verified reveal (returns exact canary, records audit without plaintext, single-use replay refused).
//   - Replace stored value (keeps same reference, bumps version, retired bytes never resolve but still screen, no equality oracle).
//   - Revoke asymmetry (agent cannot revoke person-held; agent can revoke chat-generated; person revoke is permanent).
//   - Engine restart (fresh presence.Unlocks + fresh secretcap.Service on same persisted SQLite & vault):
//     every chat starts LOCKED, and a revoked value NEVER resolves or reveals again.
func TestWEATB09_A13_PresenceUnlockRevealReplaceRevokeRestart(t *testing.T) {
	ctx := t.Context()
	tmp := t.TempDir()
	dbPath := filepath.Join(tmp, "tb09-a13.db")
	vaultPath := filepath.Join(tmp, credentialstore.VaultBasename)

	store, values, ownerID := tb09OpenStoreAndVault(t, dbPath, vaultPath)
	tb09SeedProjectAndSession(t, store, ownerID, "proj-a13", "chat-a13")

	clk := newManualClock(time.Date(2026, 11, 27, 20, 0, 0, 0, time.UTC))
	pubKey, privKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("ed25519.GenerateKey: %v", err)
	}
	broker := presence.NewBroker()
	broker.SetClock(clk.Now)
	if err := broker.Configure(base64.RawURLEncoding.EncodeToString(pubKey)); err != nil {
		t.Fatalf("broker.Configure: %v", err)
	}
	unlocks := presence.NewUnlocks(broker)
	unlocks.SetClock(clk.Now)

	fp, err := secretmatch.NewFingerprinter(bytes.Repeat([]byte{0x7b}, 32))
	if err != nil {
		t.Fatalf("NewFingerprinter: %v", err)
	}

	svc := secretcap.NewWithStore(store, values, nil)
	svc.Reveals.SetPresence(broker)
	svc.SetUnlocks(unlocks)
	svc.SetFingerprinter(fp)

	canaryV1 := tb09MintCanary("a13_v1")
	canaryV2 := tb09MintCanary("a13_v2")
	canaryKeep := tb09MintCanary("a13_keep")

	// Create person-held secret + second persistent person-held secret + chat-generated secret.
	heldSecret, err := svc.CreateSettingsSecret(ctx, secretcap.CreateSettingsSecretRequest{
		ProjectID:   "proj-a13",
		OperationID: "a13-held-1",
		PersonID:    ownerID,
		Name:        "Production API Token",
		Purpose:     "verify unlock reveal replace revoke",
		Value:       canaryV1,
	})
	if err != nil {
		t.Fatalf("CreateSettingsSecret(held): %v", err)
	}
	keepSecret, err := svc.CreateSettingsSecret(ctx, secretcap.CreateSettingsSecretRequest{
		ProjectID:   "proj-a13",
		OperationID: "a13-keep-1",
		PersonID:    ownerID,
		Name:        "Surviving Token",
		Purpose:     "verify post-restart lock enforcement",
		Value:       canaryKeep,
	})
	if err != nil {
		t.Fatalf("CreateSettingsSecret(keep): %v", err)
	}
	chatGen, err := svc.Generate(ctx, secretcap.GenerateRequest{
		ProjectID:     "proj-a13",
		ChatSessionID: "chat-a13",
		SessionID:     "chat-a13",
		OperationID:   "a13-gen-1",
		Name:          "Chat Temp Key",
		Scope:         secretcap.ScopeChat,
	})
	if err != nil {
		t.Fatalf("Generate(chat): %v", err)
	}

	// 1. Unlock gating: unreleased -> HeldUnreleasedError; released while locked -> ErrVaultLocked; unlocked -> succeeds.
	res1, err := svc.Resolve(ctx, map[string]any{"header": heldSecret.Reference}, secretcap.ResolveContext{
		ProjectID:     "proj-a13",
		ChatSessionID: "chat-a13",
		SessionID:     "chat-a13",
		ToolName:      "http_request",
		ToolCallID:    "tc-1",
	})
	if err != nil {
		t.Fatalf("Resolve(v1): %v", err)
	}
	var unreleasedErr *secretcap.HeldUnreleasedError
	if _, err := res1.PrepareDelivery(ctx, func(string) bool { return true }); !errors.As(err, &unreleasedErr) {
		t.Fatalf("PrepareDelivery without release = %v, want HeldUnreleasedError", err)
	}
	res1.ApproveRelease(secretcap.Release{
		Fingerprints: []secretmatch.SecretFingerprint{fp.Fingerprint(canaryV1)},
		Recipients:   []secretmatch.Recipient{{ID: "http://127.0.0.1:8080", Surface: secretmatch.SurfaceHTTPRequest}},
	})
	if _, err := res1.PrepareDelivery(ctx, func(string) bool { return true }); !errors.Is(err, secretcap.ErrVaultLocked) {
		t.Fatalf("PrepareDelivery while chat locked = %v, want ErrVaultLocked", err)
	}

	opened := presence.NewUnlock("chat-a13", presence.Verified{
		ChallengeID:   uuid.NewString(),
		PersonID:      ownerID,
		Authenticator: presence.AuthenticatorMacOS,
		WindowLabel:   "tb09-window",
		VerifiedAt:    clk.Now(),
	})
	if !unlocks.Open(opened) {
		t.Fatalf("unlocks.Open returned false")
	}
	delivery, err := res1.PrepareDelivery(ctx, func(string) bool { return true })
	if err != nil {
		t.Fatalf("PrepareDelivery after unlock + release failed: %v", err)
	}
	delivery.Commit(ctx)
	res1.Finish(ctx)

	// 2. Presence-verified reveal: returns exact v1 canary, single-use replay rejected.
	challenge, err := svc.Reveals.BeginReveal(ctx, "proj-a13", heldSecret.Reference, "tb09-window", ownerID)
	if err != nil {
		t.Fatalf("BeginReveal: %v", err)
	}
	sig := ed25519.Sign(privKey, presence.SigningMessage(challenge.ProofPayload, presence.AuthenticatorMacOS))
	proof := presence.Proof{
		ChallengeID:   challenge.ID,
		Authenticator: presence.AuthenticatorMacOS,
		Signature:     base64.RawURLEncoding.EncodeToString(sig),
	}
	revealRes, err := svc.Reveals.CompleteReveal(ctx, "proj-a13", heldSecret.Reference, ownerID, proof)
	if err != nil {
		t.Fatalf("CompleteReveal: %v", err)
	}
	if string(revealRes.Value) != canaryV1 || revealRes.Version != 1 || revealRes.RemaskAfterSeconds != 30 {
		t.Fatalf("CompleteReveal mismatch: version=%d remask=%d value_match=%v",
			revealRes.Version, revealRes.RemaskAfterSeconds, string(revealRes.Value) == canaryV1)
	}
	if _, err := svc.Reveals.CompleteReveal(ctx, "proj-a13", heldSecret.Reference, ownerID, proof); !errors.Is(err, presence.ErrChallengeNotFound) {
		t.Fatalf("replayed reveal proof = %v, want ErrChallengeNotFound", err)
	}

	// 3. Replace stored value: same reference, version 2, retired v1 still screens & never resolves.
	// Also verify no equality oracle: replacing with same bytes bumps version identically.
	sameMeta, err := svc.ReplaceValue(ctx, secretcap.ReplaceValueRequest{
		ProjectID: "proj-a13",
		Reference: heldSecret.Reference,
		Value:     canaryV1,
	})
	if err != nil || sameMeta.Version != 2 || sameMeta.Reference != heldSecret.Reference {
		t.Fatalf("ReplaceValue(same bytes) = %+v, err=%v", sameMeta, err)
	}
	replacedMeta, err := svc.ReplaceValue(ctx, secretcap.ReplaceValueRequest{
		ProjectID: "proj-a13",
		Reference: heldSecret.Reference,
		Value:     canaryV2,
	})
	if err != nil || replacedMeta.Version != 3 || replacedMeta.Reference != heldSecret.Reference || replacedMeta.State != sameMeta.State {
		t.Fatalf("ReplaceValue(v2) = %+v, err=%v", replacedMeta, err)
	}
	res2, err := svc.Resolve(ctx, map[string]any{"header": heldSecret.Reference}, secretcap.ResolveContext{
		ProjectID:     "proj-a13",
		ChatSessionID: "chat-a13",
	})
	if err != nil {
		t.Fatalf("Resolve after replace: %v", err)
	}
	if got := res2.Arguments["header"].(string); got != canaryV2 {
		t.Fatalf("Resolve after replace returned wrong value (retired v1 must never resolve)")
	}
	res2.Finish(ctx)

	evidenceAfterReplace, err := svc.ScreeningValues(ctx, "proj-a13", "chat-a13")
	if err != nil {
		t.Fatalf("ScreeningValues after replace: %v", err)
	}
	sawRetiredV1, sawActiveV2 := false, false
	for _, ev := range evidenceAfterReplace {
		if ev.Secret == canaryV1 && ev.Retired {
			sawRetiredV1 = true
		}
		if ev.Secret == canaryV2 && !ev.Retired && ev.Reference == heldSecret.Reference {
			sawActiveV2 = true
		}
	}
	if !sawRetiredV1 || !sawActiveV2 {
		t.Fatalf("screening evidence after replace incomplete: retiredV1=%v activeV2=%v", sawRetiredV1, sawActiveV2)
	}

	// 4. Revoke asymmetry + terminal revocation.
	if _, err := svc.RevokeByAgent(ctx, "proj-a13", "chat-a13", heldSecret.Reference); !errors.Is(err, secretcap.ErrHumanAuthored) {
		t.Fatalf("RevokeByAgent(person-held) = %v, want ErrHumanAuthored", err)
	}
	revokedGen, err := svc.RevokeByAgent(ctx, "proj-a13", "chat-a13", chatGen.Reference)
	if err != nil || revokedGen.State != secretcap.StateRevoked {
		t.Fatalf("RevokeByAgent(chat-generated) = %+v, err=%v", revokedGen, err)
	}
	revokedHeld, err := svc.RevokeProject(ctx, "proj-a13", heldSecret.Reference, ownerID)
	if err != nil || revokedHeld.State != secretcap.StateRevoked {
		t.Fatalf("RevokeProject(held) = %+v, err=%v", revokedHeld, err)
	}
	if _, err := svc.Resolve(ctx, map[string]any{"h": heldSecret.Reference}, secretcap.ResolveContext{
		ProjectID: "proj-a13", ChatSessionID: "chat-a13",
	}); !errors.Is(err, secretcap.ErrRevoked) {
		t.Fatalf("Resolve after revoke = %v, want ErrRevoked", err)
	}
	if _, err := svc.Reveals.BeginReveal(ctx, "proj-a13", heldSecret.Reference, "tb09-window", ownerID); !errors.Is(err, secretcap.ErrRevoked) {
		t.Fatalf("BeginReveal after revoke = %v, want ErrRevoked", err)
	}

	// 5. Simulate engine restart: fresh presence.Broker, fresh presence.Unlocks, fresh secretcap.Service
	// backed by the SAME persisted SQLite database and encrypted vault.
	restartedBroker := presence.NewBroker()
	restartedBroker.SetClock(clk.Now)
	if err := restartedBroker.Configure(base64.RawURLEncoding.EncodeToString(pubKey)); err != nil {
		t.Fatalf("restartedBroker.Configure: %v", err)
	}
	restartedUnlocks := presence.NewUnlocks(restartedBroker)
	restartedUnlocks.SetClock(clk.Now)

	restartedValues := credentialstoretest.Open(t, credentialstore.Slot{
		Path:      vaultPath,
		Namespace: credentialstore.NamespaceManagedSecrets,
		Context:   "wea tb09 synthetic test vault (restarted)",
	}, tb09ValidUUID)
	restartedSvc := secretcap.NewWithStore(store, restartedValues, nil)
	restartedSvc.Reveals.SetPresence(restartedBroker)
	restartedSvc.SetUnlocks(restartedUnlocks)
	restartedSvc.SetFingerprinter(fp)

	// After restart, every chat MUST be locked.
	if _, active := restartedUnlocks.Active("chat-a13"); active {
		t.Fatalf("chat-a13 remained unlocked across engine restart")
	}
	// Surviving person-held secret MUST fail delivery with ErrVaultLocked until freshly unlocked.
	resKeep, err := restartedSvc.Resolve(ctx, map[string]any{"h": keepSecret.Reference}, secretcap.ResolveContext{
		ProjectID: "proj-a13", ChatSessionID: "chat-a13",
	})
	if err != nil {
		t.Fatalf("Resolve(keepSecret) after restart: %v", err)
	}
	resKeep.ApproveRelease(secretcap.Release{
		Fingerprints: []secretmatch.SecretFingerprint{fp.Fingerprint(canaryKeep)},
		Recipients:   []secretmatch.Recipient{{ID: "http://127.0.0.1:8080", Surface: secretmatch.SurfaceHTTPRequest}},
	})
	if _, err := resKeep.PrepareDelivery(ctx, func(string) bool { return true }); !errors.Is(err, secretcap.ErrVaultLocked) {
		t.Fatalf("post-restart PrepareDelivery while locked = %v, want ErrVaultLocked", err)
	}
	resKeep.Finish(ctx)

	// Revoked secret MUST STILL fail Resolve and BeginReveal after restart.
	if _, err := restartedSvc.Resolve(ctx, map[string]any{"h": heldSecret.Reference}, secretcap.ResolveContext{
		ProjectID: "proj-a13", ChatSessionID: "chat-a13",
	}); !errors.Is(err, secretcap.ErrRevoked) {
		t.Fatalf("post-restart Resolve(revoked) = %v, want ErrRevoked", err)
	}
	if _, err := restartedSvc.Reveals.BeginReveal(ctx, "proj-a13", heldSecret.Reference, "tb09-window", ownerID); !errors.Is(err, secretcap.ErrRevoked) {
		t.Fatalf("post-restart BeginReveal(revoked) = %v, want ErrRevoked", err)
	}

	// Verify ZERO synthetic canary bytes exist anywhere in SQLite.
	tb09ScanSQLiteRO(t, dbPath, []string{canaryV1, canaryV2, canaryKeep})

	receipt := map[string]any{
		"schema":              "wea-tb09-inproc-v1",
		"case_families":       []string{"A10", "A11", "A13"},
		"post_restart_locked": true,
		"revoked_terminal":    true,
		"sqlite_canary_clean": true,
	}
	if encoded, err := json.Marshal(receipt); err == nil {
		t.Logf("WEA_TB09_RECEIPT:%s", encoded)
	}
}
