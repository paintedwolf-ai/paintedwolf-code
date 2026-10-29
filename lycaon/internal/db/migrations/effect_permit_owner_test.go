package migrations_test

import (
	"database/sql"
	"encoding/json"
	"testing"

	"github.com/lycaon/lycaon/internal/db/migrations"
	"github.com/lycaon/lycaon/internal/gate"
	"github.com/lycaon/lycaon/internal/hitl"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
	_ "modernc.org/sqlite"
)

func TestEffectPermitMigrationPreservesDeclaredAuthorityVocabulary(t *testing.T) {
	action := hitl.ProposedAction{Invocation: hitl.ActionInvocation{Tool: "command", Args: map[string]any{"command": "fixture"}}, Scope: hitl.ActionScope{SessionID: "fixture-session", ProjectID: "fixture-project", ProjectDir: "/fixture"}}
	once := hitl.CurrentActionOption()
	socket := once
	sockets := []hitl.ApprovalSocketTarget{{ApprovedPath: "/fixture/service.sock", ResolvedPath: "/fixture/service.sock"}}
	socket.Authority = []hitl.ApprovalAuthorityDelta{{Kind: hitl.AuthoritySocketPermit, SessionID: "fixture-session", ToolCallID: "actual-model-call", ActionDigest: hitl.GrantKey(action), Sockets: sockets}}
	lease := hitl.DirectIPLease{ActionDigest: hitl.GrantKey(action), RequestDigest: "requested", ConfinementDigest: "confined", DeclaredDestinations: []string{"fixture.invalid"}, CommandSummary: "fixture"}
	direct := once
	direct.Authority = []hitl.ApprovalAuthorityDelta{{Kind: hitl.AuthorityDirectIPPermit, SessionID: "fixture-session", ToolCallID: "actual-model-call", ActionDigest: lease.ActionDigest, DirectIPLease: &lease}}
	grant := hitl.ApprovalGrant{ID: "fixture-grant", Scope: hitl.ApprovalGrantScopeChat, ChatSessionID: "fixture-session", Predicate: hitl.ApprovalGrantPredicate{Category: hitl.ApprovalGrantCategoryActionSet, Pattern: "fixture"}, Title: hitl.TitleAllowForThisChat, Coverage: "fixture action", ExpiresWhen: hitl.ExpiresWhenChatDeleted, ReaskWhen: "the action changes"}
	offer := hitl.ApprovalGrantOffer{ID: grant.ID, Rung: hitl.ApprovalRungChat, Scope: grant.Scope, Title: grant.Title, Coverage: grant.Coverage, ExpiresWhen: grant.ExpiresWhen, ReaskWhen: grant.ReaskWhen, Grant: grant}
	chatOption := func(delta hitl.ApprovalAuthorityDelta, day bool) hitl.ApprovalOption {
		chat := offer
		if day {
			chat = hitl.DayRung(chat)
		}
		retainedGrant := chat.Grant
		delta.Grant, delta.ChatSessionID = &retainedGrant, "fixture-session"
		delta.TTLSeconds = chat.TTLSeconds
		if delta.Kind == hitl.AuthorityTrustDestination {
			chat.Scope, chat.Rung = hitl.ApprovalGrantScopeDevice, hitl.ApprovalRungDevice
			retainedGrant.Scope, retainedGrant.ChatSessionID = chat.Scope, ""
			delta.ChatSessionID = ""
		}
		chat.Authority = []hitl.ApprovalAuthorityDelta{delta}
		if delta.Kind == hitl.AuthorityTrustDestination {
			chat.Authority = append([]hitl.ApprovalAuthorityDelta{{Kind: hitl.AuthorityCurrentAction}}, chat.Authority...)
		}
		return hitl.GrantOption(chat)
	}
	quietDecision := &gate.Decision{Primary: api.GateAuthorityMisuse, ReasonKey: "authority_misuse:fixture/command", Cited: []gate.Fact{{Gate: api.GateAuthorityMisuse, Key: "detection.rule", Value: "fixture", Source: "detection_pack"}}}
	quietOptions := append([]hitl.ApprovalOption{once}, hitl.QuietOptions(action, quietDecision, nil, nil)...)
	covered := map[hitl.AuthorityDeltaKind]bool{}
	for _, fixture := range []struct {
		name     string
		subject  hitl.ApprovalSubjectKind
		options  []hitl.ApprovalOption
		decision *gate.Decision
	}{
		{"current action", hitl.ApprovalSubjectAction, []hitl.ApprovalOption{once}, nil},
		{"generic grant", hitl.ApprovalSubjectAction, []hitl.ApprovalOption{once, hitl.GrantOption(offer)}, nil},
		{"generic day grant", hitl.ApprovalSubjectAction, []hitl.ApprovalOption{once, hitl.GrantOption(hitl.DayRung(offer))}, nil},
		{"socket once", hitl.ApprovalSubjectSocketSet, []hitl.ApprovalOption{socket}, nil},
		{"socket chat", hitl.ApprovalSubjectSocketSet, []hitl.ApprovalOption{socket, chatOption(hitl.ApprovalAuthorityDelta{Kind: hitl.AuthoritySocketChat, ActionDigest: hitl.GrantKey(action), Sockets: sockets}, false)}, nil},
		{"direct IP chat", hitl.ApprovalSubjectDirectIP, []hitl.ApprovalOption{direct, chatOption(hitl.ApprovalAuthorityDelta{Kind: hitl.AuthorityDirectIPChat, DirectIPLease: &lease}, false)}, nil},
		{"direct IP day chat", hitl.ApprovalSubjectDirectIP, []hitl.ApprovalOption{direct, chatOption(hitl.ApprovalAuthorityDelta{Kind: hitl.AuthorityDirectIPChat, DirectIPLease: &lease}, true)}, nil},
		{"write root day chat", hitl.ApprovalSubjectWriteRootSet, []hitl.ApprovalOption{chatOption(hitl.ApprovalAuthorityDelta{Kind: hitl.AuthorityWriteRootChat, WriteRoots: []string{"/fixture"}}, true)}, nil},
		{"read path chat", hitl.ApprovalSubjectReadPathSet, []hitl.ApprovalOption{chatOption(hitl.ApprovalAuthorityDelta{Kind: hitl.AuthorityReadPathChat, ReadPaths: []string{"/fixture/data"}}, false)}, nil},
		{"granted path", hitl.ApprovalSubjectWriteRootSet, []hitl.ApprovalOption{chatOption(hitl.ApprovalAuthorityDelta{Kind: hitl.AuthorityGrantedPath, GrantedPath: &hitl.GrantedPathDelta{Path: "/fixture/data", Write: true, Tree: true}}, false)}, nil},
		{"trust destination", hitl.ApprovalSubjectDestinationSet, []hitl.ApprovalOption{chatOption(hitl.ApprovalAuthorityDelta{Kind: hitl.AuthorityTrustDestination, TrustDestination: &hitl.TrustDestinationDelta{ProviderID: "fixture-provider", DestinationID: "fixture-destination", Label: "Fixture"}}, false)}, nil},
		{"local listener", hitl.ApprovalSubjectLocalListen, []hitl.ApprovalOption{once, chatOption(hitl.ApprovalAuthorityDelta{Kind: hitl.AuthorityLocalListenChat, ListenPorts: []uint16{8080}}, false)}, nil},
		{"loopback connection", hitl.ApprovalSubjectLoopbackConnect, []hitl.ApprovalOption{once, chatOption(hitl.ApprovalAuthorityDelta{Kind: hitl.AuthorityLoopbackConnectChat, ConnectPorts: []uint16{8080}}, false)}, nil},
		{"quiet", hitl.ApprovalSubjectAction, quietOptions, quietDecision},
	} {
		t.Run(fixture.name, func(t *testing.T) {
			decision := fixture.decision
			if decision == nil {
				_, decision = gate.Evaluate(gate.Facts{Stage: gate.StagePreSpawn, Ran: gate.ProducerApprovalRequest, ApprovalRequest: &gate.ApprovalRequest{Count: 1}}, gate.DefaultPosture)
			}
			primary, cited, reasons := hitl.PresentDecision(decision)
			stage := hitl.ApprovalStagePreSpawn
			if fixture.subject == hitl.ApprovalSubjectDestinationSet {
				stage = hitl.ApprovalStagePreDial
			}
			plan, err := hitl.NewApprovalPlan(action, stage, hitl.ApprovalSubject{Kind: fixture.subject, Title: "Fixture action", Targets: []hitl.ApprovalTarget{{Kind: "fixture", Label: "fixture"}}}, hitl.ApprovalPresentation{Action: "Use fixture", Impact: "Use fixture authority", Gate: primary, Cited: cited}, reasons, fixture.options, hitl.FaceContext{})
			testutil.FailErr(t, "prepare actual owner plan", err)
			testutil.FailErr(t, "validate original owner plan", plan.Validate())
			for _, option := range plan.Options {
				for _, delta := range option.Authority {
					covered[delta.Kind] = true
				}
			}
			assertRetainedApprovalPlan(t, plan)
			assertUnrecognizedAuthorityRefused(t, plan)
		})
	}
	requireAuthorityVocabularyCoverage(t, covered)
}

func assertRetainedApprovalPlan(t *testing.T, plan *hitl.ApprovalPlan) {
	t.Helper()
	original, err := json.Marshal(plan)
	testutil.FailErr(t, "encode original owner plan", err)
	payload, err := json.Marshal(map[string]any{"approval_plan": plan})
	testutil.FailErr(t, "encode checkpoint", err)
	database, err := sql.Open("sqlite", t.TempDir()+"/fixture.db")
	testutil.FailErr(t, "open isolated checkpoint", err)
	defer database.Close()
	_, err = database.Exec(`CREATE TABLE checkpoints(id TEXT PRIMARY KEY,kind TEXT,payload_json TEXT); CREATE TABLE approval_operations(checkpoint_id TEXT PRIMARY KEY); CREATE TABLE chat_grants(id TEXT PRIMARY KEY)`)
	testutil.FailErr(t, "create checkpoint table", err)
	_, err = database.Exec(`INSERT INTO checkpoints VALUES('fixture','tool_approval',?)`, string(payload))
	testutil.FailErr(t, "persist actual owner plan", err)
	tx, err := database.BeginTx(t.Context(), nil)
	testutil.FailErr(t, "begin format conversion", err)
	step := migrations.EffectPermitIdentity(migrations.Baseline{Revision: 5})
	testutil.FailErr(t, "apply actual format migration", step.Apply(t.Context(), tx))
	testutil.FailErr(t, "commit fixture conversion", tx.Commit())
	var raw, format, digest, body string
	testutil.FailErr(t, "read migrated checkpoint", database.QueryRow(`SELECT payload_json,authority_format,authority_digest,authority_body FROM checkpoints WHERE id='fixture'`).Scan(&raw, &format, &digest, &body))
	var restored struct {
		Plan hitl.ApprovalPlan `json:"approval_plan"`
	}
	testutil.FailErr(t, "read canonical owner shape", json.Unmarshal([]byte(raw), &restored))
	testutil.FailErr(t, "validate retained owner identity", restored.Plan.Validate())
	encoded, err := json.Marshal(restored.Plan)
	testutil.FailErr(t, "encode canonical restored plan", err)
	if string(original) != string(encoded) || format != "" || digest != "" || body != "" {
		t.Fatal("migration changed the canonical plan or minted native authority")
	}
	convertedOnce := false
	for _, option := range plan.Options {
		for _, delta := range option.Authority {
			convertedOnce = convertedOnce || delta.Kind == hitl.AuthoritySocketPermit || delta.Kind == hitl.AuthorityDirectIPPermit
		}
	}
	if !convertedOnce && raw != string(payload) {
		t.Fatal("migration rewrote an unchanged retained authority payload")
	}
	for _, option := range restored.Plan.Options {
		for _, delta := range option.Authority {
			if delta.ExecutionID != "" {
				t.Fatal("migration minted a native child identity")
			}
		}
	}
}
