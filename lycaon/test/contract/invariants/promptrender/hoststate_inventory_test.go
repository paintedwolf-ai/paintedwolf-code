package promptrender_test

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/extpacks"
	"github.com/lycaon/lycaon/internal/gate"
	"github.com/lycaon/lycaon/internal/prompts"
	"github.com/lycaon/lycaon/internal/sandbox"
	"github.com/lycaon/lycaon/internal/settings"
	"github.com/lycaon/lycaon/internal/toolvocab"
	"github.com/lycaon/lycaon/pkg/api"
	"github.com/lycaon/lycaon/test/contract/internal/hoststate"
	"github.com/lycaon/lycaon/test/contract/internal/interactions"
	"github.com/lycaon/lycaon/test/contract/invariants/promptrender"
)

func TestHostStateInventoryZeroUnexplainedGaps(t *testing.T) {
	ctx := t.Context()
	stock, err := promptrender.LoadStock(ctx)
	if err != nil {
		t.Fatalf("LoadStock: %v", err)
	}
	domains, err := hoststate.DefaultSeedDomains()
	if err != nil {
		t.Fatalf("DefaultSeedDomains: %v", err)
	}
	coordStates, err := hoststate.CoordinatorStates(ctx)
	if err != nil {
		t.Fatalf("CoordinatorStates: %v", err)
	}
	workerStates, err := hoststate.WorkerStates(ctx)
	if err != nil {
		t.Fatalf("WorkerStates: %v", err)
	}
	sum, err := promptrender.RunCartesianMatrix(ctx, stock)
	if err != nil {
		t.Fatalf("RunCartesianMatrix: %v", err)
	}

	profiles, err := sandbox.LoadToolProfilesWithCatalog(stock.Catalog)
	if err != nil {
		t.Fatalf("LoadToolProfilesWithCatalog: %v", err)
	}
	names, err := toolvocab.NewCatalog(stock.Schemas, profiles, toolvocab.Provenance{})
	if err != nil {
		t.Fatalf("toolvocab.NewCatalog: %v", err)
	}
	ledger, err := interactions.BuildInteractionLedger(stock.Catalog, stock.Schemas, names, nil)
	if err != nil {
		t.Fatalf("BuildInteractionLedger: %v", err)
	}

	coordAuths := make(map[string]hoststate.TurnAuthority, len(coordStates))
	workerAuths := make(map[string]hoststate.TurnAuthority, len(workerStates))
	delivered := make(map[string]interactions.DeliveredStateObservation, len(coordStates)+len(workerStates))

	approvalDir := t.TempDir()
	approvalMemo := map[gate.Posture]string{}
	standingMemo := map[string]string{}
	workerPersonaMemo := map[string]string{}
	for _, st := range coordStates {
		if st.Excluded {
			continue
		}
		auth, fErr := hoststate.FreezeTurnAuthority(ctx, st)
		if fErr != nil {
			t.Fatalf("FreezeTurnAuthority(%s): %v", st.ID, fErr)
		}
		coordAuths[st.ID] = auth
		obs, oErr := observeCoordinatorStateDelivery(ctx, t, stock, approvalDir, approvalMemo, standingMemo, st, auth)
		if oErr != nil {
			t.Fatalf("observeCoordinatorStateDelivery(%s): %v", st.ID, oErr)
		}
		delivered[st.ID] = obs
	}

	for _, ws := range workerStates {
		if ws.Excluded {
			continue
		}
		auth, fErr := hoststate.FreezeWorkerAuthority(ctx, ws)
		if fErr != nil {
			t.Fatalf("FreezeWorkerAuthority(%s): %v", ws.ID, fErr)
		}
		workerAuths[ws.ID] = auth
		obs, oErr := observeWorkerStateDelivery(ctx, t, stock, approvalDir, approvalMemo, workerPersonaMemo, ws, auth)
		if oErr != nil {
			t.Fatalf("observeWorkerStateDelivery(%s): %v", ws.ID, oErr)
		}
		delivered[ws.ID] = obs
	}

	prefix := strings.TrimSpace(os.Getenv("PW_CT08_INVENTORY_PREFIX"))
	if prefix == "" {
		prefix = "candidate"
	}
	label := strings.TrimSpace(os.Getenv("PW_CT08_CONSTRUCTOR_LABEL"))
	if label == "" {
		label = prefix
	}

	rep, err := interactions.BuildHostStateInventory(interactions.HostStateInventoryInput{
		SourceCommit:           strings.TrimSpace(os.Getenv("PW_CT08_SOURCE_COMMIT")),
		SourceTree:             strings.TrimSpace(os.Getenv("PW_CT08_SOURCE_TREE")),
		ConstructorSetLabel:    label,
		EffectiveCatalog:       stock.Catalog,
		Domains:                domains,
		CoordinatorStates:      coordStates,
		WorkerStates:           workerStates,
		CoordinatorAuthorities: coordAuths,
		WorkerAuthorities:      workerAuths,
		DeliveredObservations:  delivered,
		Coverage:               &sum.Coverage,
		Ledger:                 ledger,
	})
	if err != nil {
		t.Fatalf("BuildHostStateInventory: %v", err)
	}

	if outDir := strings.TrimSpace(os.Getenv("PW_CT08_INVENTORY_OUT_DIR")); outDir != "" {
		if mkErr := os.MkdirAll(outDir, 0o755); mkErr != nil {
			t.Fatalf("mkdir %s: %v", outDir, mkErr)
		}
		rawJSON, mErr := rep.MarshalJSONBytes()
		if mErr != nil {
			t.Fatalf("MarshalJSONBytes: %v", mErr)
		}
		if wErr := os.WriteFile(filepath.Join(outDir, prefix+"-hoststate-inventory.json"), rawJSON, 0o644); wErr != nil {
			t.Fatalf("write inventory json: %v", wErr)
		}
		if wErr := os.WriteFile(filepath.Join(outDir, prefix+"-hoststate-inventory.md"), []byte(rep.FormatMarkdown()), 0o644); wErr != nil {
			t.Fatalf("write inventory md: %v", wErr)
		}
	}

	if err := rep.ValidateZeroUnexplainedGaps(); err != nil {
		t.Fatalf("%v", err)
	}
}

func resolveApprovalThroughStore(t *testing.T, baseDir string, memo map[gate.Posture]string, requested gate.Posture) string {
	t.Helper()
	if cached, ok := memo[requested]; ok {
		return cached
	}
	if requested == "" {
		memo[requested] = string(gate.PostureStrict)
		return string(gate.PostureStrict)
	}
	parsed, err := gate.ParsePosture(string(requested))
	if err != nil {
		memo[requested] = string(gate.PostureStrict)
		return string(gate.PostureStrict)
	}
	globalPath := filepath.Join(baseDir, "approvals-"+string(parsed)+".yaml")
	projDir := filepath.Join(baseDir, "proj-"+string(parsed))
	_ = os.MkdirAll(projDir, 0o755)
	store, err := settings.NewApprovalStoreAt(globalPath)
	if err != nil {
		t.Fatalf("NewApprovalStoreAt: %v", err)
	}
	t.Cleanup(func() { store.Close() })
	if err := store.PutGlobal(settings.ApprovalConfig{Posture: gate.PostureLight}); err != nil {
		t.Fatalf("PutGlobal: %v", err)
	}
	if err := store.PutProject(projDir, settings.ApprovalConfig{Posture: parsed}); err != nil {
		t.Fatalf("PutProject: %v", err)
	}
	resolved := string(store.EffectivePosture(settings.ProjectRef{ID: "proj-" + string(parsed), Dir: projDir}))
	memo[requested] = resolved
	return resolved
}

func observeCoordinatorStateDelivery(
	ctx context.Context,
	t *testing.T,
	s *promptrender.Stock,
	approvalDir string,
	approvalMemo map[gate.Posture]string,
	standingMemo map[string]string,
	st hoststate.CoordinatorState,
	auth hoststate.TurnAuthority,
) (interactions.DeliveredStateObservation, error) {
	t.Helper()
	memoKey := st.PackOverlayID + "|" + st.TurnProfile.SurfaceID + "|" + auth.Reader.Fingerprint()
	text, hit := standingMemo[memoKey]
	if !hit {
		var err error
		text, err = promptrender.RenderCoordinatorStandingForTest(ctx, s, st, "")
		if err != nil {
			return interactions.DeliveredStateObservation{}, err
		}
		standingMemo[memoKey] = text
	}
	facts := auth.Reader.Facts()
	reviews, _ := facts["reviews_confined_writes"].(bool)
	hasGit, _ := facts["has_git"].(bool)
	anyGit, _ := facts["has_any_git_root"].(bool)
	multiRoot, _ := facts["multi_root"].(bool)
	webOn, _ := facts["web_search_enabled"].(bool)
	extHidden, _ := facts["command_external_hidden"].(bool)
	mode, _ := facts["session_mode"].(string)
	vis := false
	if cm, ok := facts["caps"].(map[string]any); ok {
		vis, _ = cm["vision"].(bool)
	}

	var activated []string
	for name, ok := range st.Loaded {
		if ok && slices.Contains(auth.Offered, name) {
			activated = append(activated, name)
		}
	}
	slices.Sort(activated)
	remaining := slices.Clone(auth.Reader.Requestable())
	slices.Sort(remaining)

	return interactions.DeliveredStateObservation{
		StateID:                st.ID,
		Recipient:              "coordinator",
		ResolvedApprovalToken:  resolveApprovalThroughStore(t, approvalDir, approvalMemo, st.ApprovalPosture),
		ResolvedSessionMode:    mode,
		ReviewsConfinedWrites:  reviews,
		PrimaryRootGit:         hasGit,
		AnyRootGit:             anyGit,
		MultiRoot:              multiRoot,
		WebSearchEnabled:       webOn,
		VisionEnabled:          vis,
		CommandExternalHidden:  extHidden,
		ActivatedDeferredTools: activated,
		RemainingDeferredTools: remaining,
		RenderedMarkers:        extractRenderedMarkers(text, auth.Offered, auth.Reader.Requestable()),
	}, nil
}

func observeWorkerStateDelivery(
	ctx context.Context,
	t *testing.T,
	s *promptrender.Stock,
	approvalDir string,
	approvalMemo map[gate.Posture]string,
	personaMemo map[string]string,
	ws hoststate.WorkerState,
	auth hoststate.TurnAuthority,
) (interactions.DeliveredStateObservation, error) {
	t.Helper()
	facts := auth.Reader.Facts()
	reviews, _ := facts["reviews_confined_writes"].(bool)
	hasGit, _ := facts["has_git"].(bool)
	anyGit, _ := facts["has_any_git_root"].(bool)
	multiRoot, _ := facts["multi_root"].(bool)
	webOn, _ := facts["web_search_enabled"].(bool)
	extHidden, _ := facts["command_external_hidden"].(bool)
	mode, _ := facts["session_mode"].(string)
	vis := false
	if cm, ok := facts["caps"].(map[string]any); ok {
		vis, _ = cm["vision"].(bool)
	}

	var rendered string
	mutInAsk := false
	if ws.ParentSessionMode == api.SessionModeAsk {
		if mutCapable, ok := prompts.AgentMutationCapable(ws.AgentID); ok && mutCapable {
			mutInAsk = true
		}
	}
	if !mutInAsk {
		memoKey := ws.Overlay.ID + "|" + ws.AgentID + "|" + auth.Reader.Fingerprint()
		if cached, hit := personaMemo[memoKey]; hit {
			rendered = cached
		} else {
			eng := s.Engine()
			if ws.Overlay.Catalog != nil && ws.Overlay.Catalog != s.Catalog {
				eng = prompts.NewFileTemplateEngineLayers(prompts.PromptLayers{Catalog: ws.Overlay.Catalog})
			}
			text, err := prompts.RenderPersona(ctx, eng, ws.AgentID, auth.Reader, nil)
			if err != nil {
				return interactions.DeliveredStateObservation{}, err
			}
			personaMemo[memoKey] = text
			rendered = text
		}
	}

	var activated []string
	for name, ok := range ws.Loaded {
		if ok && slices.Contains(auth.Offered, name) {
			activated = append(activated, name)
		}
	}
	slices.Sort(activated)
	remaining := slices.Clone(auth.Reader.Requestable())
	slices.Sort(remaining)

	_ = extpacks.EmptyDesired
	return interactions.DeliveredStateObservation{
		StateID:                ws.ID,
		Recipient:              "worker",
		ResolvedApprovalToken:  resolveApprovalThroughStore(t, approvalDir, approvalMemo, ws.ApprovalPosture),
		ResolvedSessionMode:    mode,
		ReviewsConfinedWrites:  reviews,
		PrimaryRootGit:         hasGit,
		AnyRootGit:             anyGit,
		MultiRoot:              multiRoot,
		WebSearchEnabled:       webOn,
		VisionEnabled:          vis,
		CommandExternalHidden:  extHidden,
		ActivatedDeferredTools: activated,
		RemainingDeferredTools: remaining,
		RenderedMarkers:        extractRenderedMarkers(rendered, auth.Offered, auth.Reader.Requestable()),
	}, nil
}

func extractRenderedMarkers(text string, offered, requestable []string) []string {
	var markers []string
	if strings.Contains(text, "Confined programs can write standard caches and package stores.") {
		markers = append(markers, "standard_caches_unreviewed")
	}
	if strings.Contains(text, "Declared writes outside these folders and scratch are reviewed.") {
		markers = append(markers, "outside_writes_reviewed")
	}
	if strings.Contains(text, "Qualitative appearance (visual self-review)") {
		markers = append(markers, "visual_self_review_present")
	} else {
		markers = append(markers, "visual_self_review_omitted")
	}
	if strings.Contains(text, "external -- NAME") {
		markers = append(markers, "external_selector_shown")
	} else {
		markers = append(markers, "external_selector_hidden")
	}
	if slices.Contains(offered, "web_search") || slices.Contains(requestable, "web_search") {
		markers = append(markers, "web_tools_reachable")
	} else {
		markers = append(markers, "web_tools_withheld")
	}
	return markers
}
