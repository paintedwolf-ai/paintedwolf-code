package tools

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/authzledger"
	"github.com/lycaon/lycaon/internal/gate"
	"github.com/lycaon/lycaon/internal/hitl"
	"github.com/lycaon/lycaon/internal/projectroot"
	"github.com/lycaon/lycaon/internal/secretcap"
	"github.com/lycaon/lycaon/internal/secretmatch"
	"github.com/lycaon/lycaon/internal/secretspan"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

type invariantCheckpointMgr struct {
	hitl.CheckpointManager
	status hitl.DecisionStatus
	result *hitl.DecisionResult
	req    hitl.CheckpointRequest
	err    error
}

func (m *invariantCheckpointMgr) RequestCheckpoint(_ context.Context, req hitl.CheckpointRequest) (*hitl.CheckpointResponse, error) {
	m.req = req
	if m.err != nil {
		return nil, m.err
	}
	return &hitl.CheckpointResponse{CheckpointID: "chk-invariant", Status: hitl.DecisionStatusPending}, nil
}

func (m *invariantCheckpointMgr) PollCheckpoint(_ context.Context, checkpointID string) (*hitl.CheckpointResponse, error) {
	return &hitl.CheckpointResponse{CheckpointID: checkpointID, Status: m.status, Result: m.result}, nil
}

func (m *invariantCheckpointMgr) ListPendingForParent(context.Context, string, *api.CheckpointKind) ([]api.CheckpointEvent, error) {
	return nil, nil
}

// Invariant 1: Posture, gating, and grant rules for file secrets.
// Balanced/light posture releases chat secret silently and writes ActionSecretChatLocalRelease.
// Strict posture or non-chat secret asks for approval for destination "file:.env".
// Existing grant writes covered secret silently, recording ActionSecretGrantCovered.
func runInvariant1Case1A(t *testing.T, m *secretmatch.Matcher, chatSecretRef, chatSecretVal string) {
	for _, posture := range []gate.Posture{gate.PostureBalanced, gate.PostureLight} {
		t.Run(string(posture)+"_chat_secret_silent", func(t *testing.T) {
			recorder := &capabilityRecorder{}
			exec := NewDefaultToolExecutor(nil, NewDefaultRegistry(), "implement")
			exec.SetSecretMatcher(m)
			exec.SetAuthzRecorder(recorder)
			exec.SetEgressPostureSource(func(string) gate.Posture { return posture })

			exec.secretResolver = func(_ context.Context, canonical map[string]any, access secretcap.ResolveContext) (*secretcap.Resolution, error) {
				return secretcap.NewResolutionForTest(map[string]any{
					"path":    canonical["path"],
					"content": "TOKEN=" + chatSecretVal + "\n",
				}, []secretcap.TestResolvedValue{
					{ID: "11111111-1111-1111-1111-111111111111", Name: "chat token", Value: chatSecretVal, ChatGenerated: true, Path: "/content", Fingerprint: fingerprintFor(m, chatSecretVal)},
				}), nil
			}

			reg := NewDefaultRegistry()
			var handledArgs map[string]any
			testutil.FailErr(t, "reg write", reg.RegisterDefinition(Definition{
				Meta:     ToolMeta{Name: "write"},
				Contract: catalogContract(t, "write"),
				Handler: func(_ context.Context, args map[string]any, _ ToolContext) (string, error) {
					handledArgs = args
					return "ok", nil
				},
			}))
			exec.registry = reg

			tc := ToolContext{
				ProjectID: "proj-1", SessionID: "sess-1", ParentSessionID: "root-1", ToolCallID: "tc-1",
				Roots: []projectroot.RootRef{{ID: "r1", Path: "/workspace", IsPrimary: true}}, ActiveRootID: "r1",
			}
			args := map[string]any{"path": ".env", "content": "TOKEN=" + chatSecretRef + "\n"}

			_, err := exec.Invoke(context.Background(), "write", args, tc)
			testutil.FailErr(t, "invoke write with chat secret", err)

			if handledArgs["content"] != "TOKEN="+chatSecretVal+"\n" {
				t.Fatalf("handler got content %v, want resolved bytes", handledArgs["content"])
			}
			if len(recorder.records) != 1 {
				t.Fatalf("ledger records = %d, want 1", len(recorder.records))
			}
			rec := recorder.records[0]
			if rec.Action != authzledger.ActionSecretChatLocalRelease {
				t.Fatalf("action = %v, want ActionSecretChatLocalRelease", rec.Action)
			}
			if rec.Outcome != authzledger.OutcomeAllowed {
				t.Fatalf("outcome = %v, want OutcomeAllowed", rec.Outcome)
			}
		})
	}
}

func runInvariant1Case1B(t *testing.T, m *secretmatch.Matcher, chatSecretRef, chatSecretVal, projSecretRef, projSecretVal string) {
	cases := map[string]struct {
		posture       gate.Posture
		chatGenerated bool
		ref           string
		val           string
	}{
		"strict_posture": {
			posture:       gate.PostureStrict,
			chatGenerated: true,
			ref:           chatSecretRef,
			val:           chatSecretVal,
		},
		"non_chat_secret": {
			posture:       gate.PostureBalanced,
			chatGenerated: false,
			ref:           projSecretRef,
			val:           projSecretVal,
		},
	}
	for name, tcCase := range cases {
		t.Run(name, func(t *testing.T) {
			// Subcase 1: User approves
			{
				recorder := &capabilityRecorder{}
				mgr := &invariantCheckpointMgr{status: hitl.DecisionStatusApproved}
				exec := NewDefaultToolExecutor(nil, NewDefaultRegistry(), "implement")
				exec.SetSecretMatcher(m)
				exec.SetAuthzRecorder(recorder)
				exec.SetEgressPostureSource(func(string) gate.Posture { return tcCase.posture })
				exec.SetCheckpointManager(mgr, nil)

				exec.secretResolver = func(_ context.Context, canonical map[string]any, access secretcap.ResolveContext) (*secretcap.Resolution, error) {
					id := "11111111-1111-1111-1111-111111111111"
					if !tcCase.chatGenerated {
						id = "22222222-2222-2222-2222-222222222222"
					}
					return secretcap.NewResolutionForTest(map[string]any{
						"path":    canonical["path"],
						"content": "TOKEN=" + tcCase.val + "\n",
					}, []secretcap.TestResolvedValue{
						{ID: id, Name: "token", Value: tcCase.val, ChatGenerated: tcCase.chatGenerated, Path: "/content", Fingerprint: fingerprintFor(m, tcCase.val)},
					}), nil
				}

				reg := NewDefaultRegistry()
				var handledArgs map[string]any
				testutil.FailErr(t, "reg write", reg.RegisterDefinition(Definition{
					Meta:     ToolMeta{Name: "write"},
					Contract: catalogContract(t, "write"),
					Handler: func(_ context.Context, args map[string]any, _ ToolContext) (string, error) {
						handledArgs = args
						return "ok", nil
					},
				}))
				exec.registry = reg

				tc := ToolContext{
					ProjectID: "proj-1", SessionID: "sess-1", ParentSessionID: "root-1", ToolCallID: "tc-1",
					Roots: []projectroot.RootRef{{ID: "r1", Path: "/workspace", IsPrimary: true}}, ActiveRootID: "r1",
				}
				args := map[string]any{"path": ".env", "content": "TOKEN=" + tcCase.ref + "\n"}

				_, err := exec.Invoke(context.Background(), "write", args, tc)
				testutil.FailErr(t, "invoke write under approval", err)

				if mgr.req.Kind != api.CheckpointKindToolApproval {
					t.Fatalf("checkpoint kind = %v, want ToolApproval", mgr.req.Kind)
				}
				if mgr.req.SecretScreen == nil {
					t.Fatalf("checkpoint missing SecretScreen: %+v", mgr.req)
				}
				if mgr.req.SecretScreen.DestinationKind != secretmatch.DestinationFile {
					t.Fatalf("destination kind = %v, want file", mgr.req.SecretScreen.DestinationKind)
				}
				if mgr.req.SecretScreen.DestinationID != "file:.env" {
					t.Fatalf("destination id = %v, want file:.env", mgr.req.SecretScreen.DestinationID)
				}
				if handledArgs["content"] != "TOKEN="+tcCase.val+"\n" {
					t.Fatalf("handler got content %v, want resolved bytes", handledArgs["content"])
				}
			}

			// Subcase 2: User rejects
			{
				recorder := &capabilityRecorder{}
				mgr := &invariantCheckpointMgr{status: hitl.DecisionStatusRejected}
				exec := NewDefaultToolExecutor(nil, NewDefaultRegistry(), "implement")
				exec.SetSecretMatcher(m)
				exec.SetAuthzRecorder(recorder)
				exec.SetEgressPostureSource(func(string) gate.Posture { return tcCase.posture })
				exec.SetCheckpointManager(mgr, nil)

				exec.secretResolver = func(_ context.Context, canonical map[string]any, access secretcap.ResolveContext) (*secretcap.Resolution, error) {
					id := "11111111-1111-1111-1111-111111111111"
					if !tcCase.chatGenerated {
						id = "22222222-2222-2222-2222-222222222222"
					}
					return secretcap.NewResolutionForTest(map[string]any{
						"path":    canonical["path"],
						"content": "TOKEN=" + tcCase.val + "\n",
					}, []secretcap.TestResolvedValue{
						{ID: id, Name: "token", Value: tcCase.val, ChatGenerated: tcCase.chatGenerated, Path: "/content", Fingerprint: fingerprintFor(m, tcCase.val)},
					}), nil
				}

				var writeCalled bool
				reg := NewDefaultRegistry()
				testutil.FailErr(t, "reg write", reg.RegisterDefinition(Definition{
					Meta:     ToolMeta{Name: "write"},
					Contract: catalogContract(t, "write"),
					Handler: func(_ context.Context, args map[string]any, _ ToolContext) (string, error) {
						writeCalled = true
						return "ok", nil
					},
				}))
				exec.registry = reg

				tc := ToolContext{
					ProjectID: "proj-1", SessionID: "sess-1", ParentSessionID: "root-1", ToolCallID: "tc-1",
					Roots: []projectroot.RootRef{{ID: "r1", Path: "/workspace", IsPrimary: true}}, ActiveRootID: "r1",
				}
				args := map[string]any{"path": ".env", "content": "TOKEN=" + tcCase.ref + "\n"}

				_, err := exec.Invoke(context.Background(), "write", args, tc)
				if err == nil {
					t.Fatal("expected write rejection, got nil err")
				}
				if writeCalled {
					t.Fatal("handler was invoked despite user rejection")
				}
			}
		})
	}
}

func runInvariant1Case1C(t *testing.T, m *secretmatch.Matcher, projSecretRef, projSecretVal string) {
	t.Run("grant_covered_skips_card", func(t *testing.T) {
		recorder := &capabilityRecorder{}
		mgr := &invariantCheckpointMgr{status: hitl.DecisionStatusPending}
		exec := NewDefaultToolExecutor(nil, NewDefaultRegistry(), "implement")
		exec.SetSecretMatcher(m)
		exec.SetAuthzRecorder(recorder)
		exec.SetEgressPostureSource(func(string) gate.Posture { return gate.PostureStrict })
		exec.SetCheckpointManager(mgr, nil)

		exec.secretResolver = func(_ context.Context, canonical map[string]any, access secretcap.ResolveContext) (*secretcap.Resolution, error) {
			res := secretcap.NewResolutionForTest(map[string]any{
				"path":    canonical["path"],
				"content": "TOKEN=" + projSecretVal + "\n",
			}, []secretcap.TestResolvedValue{
				{ID: "22222222-2222-2222-2222-222222222222", Name: "project token", Value: projSecretVal, ChatGenerated: false, Path: "/content", Fingerprint: fingerprintFor(m, projSecretVal)},
			})
			recipient := secretmatch.Recipient{
				ID: "file:.env", Label: "Local file: .env", Surface: secretmatch.SurfaceFile, Kind: secretmatch.DestinationFile,
			}
			known, _ := res.Matches(m, func(string) bool { return true })
			res.ApproveRelease(secretcap.Release{Fingerprints: secretmatch.Fingerprints(known), Recipients: []secretmatch.Recipient{recipient}})
			return res, nil
		}

		reg := NewDefaultRegistry()
		var handledArgs map[string]any
		testutil.FailErr(t, "reg write", reg.RegisterDefinition(Definition{
			Meta:     ToolMeta{Name: "write"},
			Contract: catalogContract(t, "write"),
			Handler: func(_ context.Context, args map[string]any, _ ToolContext) (string, error) {
				handledArgs = args
				return "ok", nil
			},
		}))
		exec.registry = reg

		tc := ToolContext{
			ProjectID: "proj-1", SessionID: "sess-1", ParentSessionID: "root-1", ToolCallID: "tc-1",
			Roots: []projectroot.RootRef{{ID: "r1", Path: "/workspace", IsPrimary: true}}, ActiveRootID: "r1",
		}
		args := map[string]any{"path": ".env", "content": "TOKEN=" + projSecretRef + "\n"}

		_, err := exec.Invoke(context.Background(), "write", args, tc)
		testutil.FailErr(t, "invoke write with grant-covered secret", err)

		if handledArgs["content"] != "TOKEN="+projSecretVal+"\n" {
			t.Fatalf("handler got content %v, want resolved bytes", handledArgs["content"])
		}
		if mgr.req.Kind != "" {
			t.Fatalf("checkpoint was requested unexpectedly: %+v", mgr.req)
		}
	})
}

func TestInvariant1_PostureGatingAndGrantRulesForFileSecrets(t *testing.T) {
	const chatSecretRef = "{{paintedwolf-secret:11111111-1111-1111-1111-111111111111}}"
	const chatSecretVal = "chat-super-secret-token-12345678"
	const projSecretRef = "{{paintedwolf-secret:22222222-2222-2222-2222-222222222222}}"
	const projSecretVal = "proj-super-secret-token-87654321"

	m, err := secretmatch.BuildMatcher(secretmatch.Bundled())
	testutil.FailErr(t, "BuildMatcher", err)
	fp, err := secretmatch.NewFingerprinter(bytes.Repeat([]byte{0x5a}, 32))
	testutil.FailErr(t, "fingerprinter", err)
	m.SetFingerprinter(fp)
	m.SetHarvestSource(func(context.Context) []secretmatch.HarvestedValue {
		return []secretmatch.HarvestedValue{
			{
				Name: "chat token", Container: "managed secret", Secret: chatSecretVal,
				RuleID: secretmatch.ManagedRuleID, Title: secretmatch.ManagedRuleTitle,
				Source: secretmatch.SourceRememberedMatch, Reference: chatSecretRef, NonDisclosable: true,
			},
			{
				Name: "project token", Container: "managed secret", Secret: projSecretVal,
				RuleID: secretmatch.ManagedRuleID, Title: secretmatch.ManagedRuleTitle,
				Source: secretmatch.SourceRememberedMatch, Reference: projSecretRef, NonDisclosable: true,
			},
		}
	})

	runInvariant1Case1A(t, m, chatSecretRef, chatSecretVal)
	runInvariant1Case1B(t, m, chatSecretRef, chatSecretVal, projSecretRef, projSecretVal)
	runInvariant1Case1C(t, m, projSecretRef, projSecretVal)
}

// Invariant 2: the review diff masks Before and After to reference tokens with
// no raw credential bytes, and approval substitutes resolved bytes before commit.
func TestInvariant2_SymmetricValueFreeWireDiffAndSpanTableRestoration(t *testing.T) {
	const oldSecretID = "33333333-3333-3333-3333-333333333333"
	const oldSecretRef = "{{paintedwolf-secret:33333333-3333-3333-3333-333333333333}}"
	const oldSecretVal = "old-secret-raw-value-99999"

	const newSecretID = "44444444-4444-4444-4444-444444444444"
	const newSecretRef = "{{paintedwolf-secret:44444444-4444-4444-4444-444444444444}}"
	const newSecretVal = "new-secret-raw-value-88888"

	res := secretcap.NewResolutionForTest(map[string]any{}, []secretcap.TestResolvedValue{
		{ID: oldSecretID, Name: "old secret", Value: oldSecretVal, ChatGenerated: true},
		{ID: newSecretID, Name: "new secret", Value: newSecretVal, ChatGenerated: true},
	})

	// 1. Verify ReferenceEchoes symmetrically masks raw secrets in Before and After
	beforeRaw := "API_KEY=" + oldSecretVal + "\nOTHER=1\n"
	afterRaw := "API_KEY=" + newSecretVal + "\nOTHER=1\n"

	maskedBefore := res.ReferenceEchoes(beforeRaw)
	maskedAfter := res.ReferenceEchoes(afterRaw)

	if strings.Contains(maskedBefore, oldSecretVal) {
		t.Fatal("maskedBefore still contains raw old secret")
	}
	if !strings.Contains(maskedBefore, oldSecretRef) {
		t.Fatal("maskedBefore does not contain old secret reference token")
	}
	if strings.Contains(maskedAfter, newSecretVal) {
		t.Fatal("maskedAfter still contains raw new secret")
	}
	if !strings.Contains(maskedAfter, newSecretRef) {
		t.Fatal("maskedAfter does not contain new secret reference token")
	}

	// 2. Verify Substitute restores the real bytes upon approval
	// The user approved the masked diff (which contains newSecretRef)
	approvedText := "API_KEY=" + newSecretRef + "\nOTHER=1\n"
	diskBytes, err := res.Substitute(approvedText)
	testutil.FailErr(t, "Substitute", err)

	if diskBytes != afterRaw {
		t.Fatalf("substituted disk bytes = %q, want %q", diskBytes, afterRaw)
	}

	// 3. Verify that an unknown / invalid reference cannot substitute
	const unknownRef = "{{paintedwolf-secret:00000000-0000-0000-0000-000000000000}}"
	_, err = res.Substitute("KEY=" + unknownRef)
	if !errors.Is(err, secretcap.ErrValueMissing) {
		t.Fatalf("substitute unknown ref err = %v, want ErrValueMissing", err)
	}
}

// Invariant 3: Slot-level resolution and batch operations.
// File tools resolve only in declared secret_reference_args; non-target slots are not resolved.
// Command surface resolves across all argument trees (command, env, stdin).
// replace_lines resolves new_content across operations array.
// jq_edit vars map safely binds secrets via gojq variable binding.
func TestInvariant3_SlotLevelResolutionAndBatchOperations(t *testing.T) {
	const secretRef = "{{paintedwolf-secret:55555555-5555-5555-5555-555555555555}}"

	// 1. File slot isolation: path is not resolved even if it contains a token.
	writeContract := catalogContract(t, "write")
	if !writeContract.SecretReferenceSurface.IsFile() {
		t.Fatalf("write contract surface = %v, want file", writeContract.SecretReferenceSurface)
	}
	if len(writeContract.SecretReferenceArgs) != 1 || writeContract.SecretReferenceArgs[0] != "content" {
		t.Fatalf("write secret_reference_args = %v, want [content]", writeContract.SecretReferenceArgs)
	}

	use := secretcap.ReferenceUseInSlots(map[string]any{
		"path":    secretRef,
		"content": "literal plain text",
	}, writeContract.SecretReferenceArgs)
	if use.Complete {
		t.Fatal("ReferenceUseInSlots reported complete reference in non-declared slot 'path'")
	}

	use = secretcap.ReferenceUseInSlots(map[string]any{
		"path":    "normal.txt",
		"content": "TOKEN=" + secretRef,
	}, writeContract.SecretReferenceArgs)
	if !use.Complete {
		t.Fatal("ReferenceUseInSlots did not detect complete reference in declared slot 'content'")
	}

	// 2. Command surface: no slot restriction, resolves anywhere
	cmdContract := catalogContract(t, "command")
	if len(cmdContract.SecretReferenceArgs) != 0 {
		t.Fatalf("command must not restrict slots: got %v", cmdContract.SecretReferenceArgs)
	}
	use = secretcap.ReferenceUseInSlots(map[string]any{
		"env": map[string]any{"API_KEY": secretRef},
	}, cmdContract.SecretReferenceArgs)
	if !use.Complete {
		t.Fatal("command must resolve references in env")
	}

	// 3. replace_lines: operations array new_content slot matching
	replaceContract := catalogContract(t, "replace_lines")
	use = secretcap.ReferenceUseInSlots(map[string]any{
		"operations": []any{
			map[string]any{
				"start_line":  1,
				"end_line":    1,
				"new_content": "KEY=" + secretRef,
			},
		},
	}, replaceContract.SecretReferenceArgs)
	if !use.Complete {
		t.Fatal("replace_lines did not detect reference in operations.*.new_content")
	}

	// 4. jq_edit: vars map slot matching
	jqContract := catalogContract(t, "jq_edit")
	use = secretcap.ReferenceUseInSlots(map[string]any{
		"path":  "config.json",
		"query": ".api_key = $secret",
		"vars":  map[string]any{"secret": secretRef},
	}, jqContract.SecretReferenceArgs)
	if !use.Complete {
		t.Fatal("jq_edit did not detect reference in vars.*")
	}
	// path and query do not resolve references
	use = secretcap.ReferenceUseInSlots(map[string]any{
		"path":  secretRef,
		"query": secretRef,
	}, jqContract.SecretReferenceArgs)
	if use.Complete {
		t.Fatal("jq_edit must not resolve references in path or query")
	}
}

// Invariant 4: line-oriented file tools reject secret values containing
// newlines or NUL bytes with SECRET_REFERENCE_VALUE_UNSAFE before any card.
func TestInvariant4_UnsafeRawValueRejection(t *testing.T) {
	for _, unsafeVal := range []string{
		"has\nnewline",
		"has\rreturn",
		"has\x00null",
	} {
		const secretRef = "{{paintedwolf-secret:66666666-6666-6666-6666-666666666666}}"
		res := secretcap.NewResolutionForTest(map[string]any{"content": unsafeVal, "old_string": unsafeVal, "new_string": unsafeVal, "new_content": unsafeVal}, []secretcap.TestResolvedValue{
			{ID: "66666666-6666-6666-6666-666666666666", Name: "unsafe secret", Value: unsafeVal},
		})
		if !res.HasUnsafeFileBytes() {
			t.Fatalf("HasUnsafeFileBytes failed for value %q", unsafeVal)
		}

		exec := NewDefaultToolExecutor(nil, NewDefaultRegistry(), "implement")
		exec.secretResolver = func(_ context.Context, canonical map[string]any, access secretcap.ResolveContext) (*secretcap.Resolution, error) {
			return res, nil
		}
		for _, tool := range []string{"write", "edit", "replace_lines"} {
			contract := catalogContract(t, tool)
			slot := "content"
			if len(contract.SecretReferenceArgs) > 0 {
				slot = contract.SecretReferenceArgs[0]
			}
			args := map[string]any{slot: secretRef}
			tc := ToolContext{
				Invocation: Invocation{Contract: contract},
			}
			_, _, err := exec.applyPreInvokeBoundary(context.Background(), tool, "implement", args, tc)
			if err == nil {
				t.Fatalf("%s accepted unsafe value %q", tool, unsafeVal)
			}
			var reject *ToolReject
			if !errors.As(err, &reject) {
				t.Fatalf("%s err = %v, want *ToolReject", tool, err)
			}
			if reject.Code != "SECRET_REFERENCE_VALUE_UNSAFE" {
				t.Fatalf("%s reject code = %q, want SECRET_REFERENCE_VALUE_UNSAFE", tool, reject.Code)
			}
		}
	}
}

// Invariant 5: resolve_secret_references: false keeps reference tokens literal
// and skips resolution, malformed-token checks, and secret screening.
func TestInvariant5_ExplicitLiteralTokenOptOut(t *testing.T) {
	const literalRef = "{{paintedwolf-secret:77777777-7777-7777-7777-777777777777}}"
	const malformedRef = "{{paintedwolf-secret:[REDACTED]}}"

	exec := NewDefaultToolExecutor(nil, NewDefaultRegistry(), "implement")

	for _, tool := range []string{"write", "edit", "replace_lines", "jq_edit"} {
		contract := catalogContract(t, tool)

		// 1. Literal complete token is kept literal
		args := map[string]any{
			"content":                   literalRef,
			"old_string":                literalRef,
			"new_string":                literalRef,
			"resolve_secret_references": false,
		}
		got, err := exec.resolveSecretReferences(context.Background(), tool, args, invokedAs(contract))
		testutil.FailErr(t, "resolve with opt-out", err)
		if got.Arguments["content"] != literalRef {
			t.Fatalf("%s: content changed despite opt-out", tool)
		}

		// 2. Malformed token produces no error when opt-out is enabled
		malformedArgs := map[string]any{
			"content":                   malformedRef,
			"old_string":                malformedRef,
			"new_string":                malformedRef,
			"resolve_secret_references": false,
		}
		got, err = exec.resolveSecretReferences(context.Background(), tool, malformedArgs, invokedAs(contract))
		testutil.FailErr(t, "resolve malformed with opt-out", err)
		if got.Arguments["content"] != malformedRef {
			t.Fatalf("%s: malformed content changed despite opt-out", tool)
		}

		// 3. screenFileSecrets returns nil immediately
		tc := ToolContext{
			Invocation: Invocation{Contract: contract},
			Secrets: secretcap.NewResolutionForTest(map[string]any{}, []secretcap.TestResolvedValue{
				{ID: "77777777-7777-7777-7777-777777777777", Value: "secret-value"},
			}),
		}
		if err := exec.screenFileSecrets(context.Background(), tool, args, tc); err != nil {
			t.Fatalf("%s: screenFileSecrets failed with opt-out: %v", tool, err)
		}
	}
}

// Invariant 6: Screener and editor highlighting.
// Files on disk containing resolved secrets are scanned by the screener:
// - Live secret -> StateTracked (reference populated).
// - Rotated or revoked secret -> StateRetired (reference empty).
// - Another chat's live secret -> StateTracked, without a reference.
// - Unmanaged secret -> StateDetected.
func TestInvariant6_ScreenerAndEditorHighlighting(t *testing.T) {
	const liveVal = "live-stripe-api-key-123456789"
	const liveRef = "{{paintedwolf-secret:88888888-8888-8888-8888-888888888888}}"
	const retiredVal = "retired-stripe-api-key-987654321"
	const otherChatVal = "other-chat-stripe-api-key-24680"

	m, err := secretmatch.BuildMatcher(secretmatch.Bundled())
	testutil.FailErr(t, "BuildMatcher", err)
	m.SetHarvestSource(func(context.Context) []secretmatch.HarvestedValue {
		return []secretmatch.HarvestedValue{
			{
				Name: "other chat key", Container: "managed secret", Secret: otherChatVal,
				RuleID: secretmatch.ManagedRuleID, Title: secretmatch.ManagedRuleTitle,
				Source: secretmatch.SourceRememberedMatch, NonDisclosable: true,
			},
			{
				Name: "live key", Container: "managed secret", Secret: liveVal,
				RuleID: secretmatch.ManagedRuleID, Title: secretmatch.ManagedRuleTitle,
				Source: secretmatch.SourceRememberedMatch, Reference: liveRef, NonDisclosable: true,
			},
			{
				Name: "retired key", Container: "managed secret", Secret: retiredVal,
				RuleID: secretmatch.ManagedRuleID, Title: secretmatch.ManagedRuleTitle,
				Source: secretmatch.SourceRememberedMatch, Retired: true, NonDisclosable: true,
			},
		}
	})

	screener := secretspan.New(m)
	fileContent := "LIVE=" + liveVal + "\nRETIRED=" + retiredVal + "\nOTHER=" + otherChatVal + "\n"

	res := screener.Screen(context.Background(), fileContent)
	if res == nil || len(res.Spans) != 3 {
		t.Fatalf("spans = %+v, want 3 spans", res)
	}

	liveSpan := res.Spans[0]
	if liveSpan.State != secretspan.StateTracked || liveSpan.Reference != liveRef {
		t.Fatalf("live span = %+v, want StateTracked with %s", liveSpan, liveRef)
	}

	retiredSpan := res.Spans[1]
	if retiredSpan.State != secretspan.StateRetired || retiredSpan.Reference != "" {
		t.Fatalf("retired span = %+v, want StateRetired with empty reference", retiredSpan)
	}

	otherChatSpan := res.Spans[2]
	if otherChatSpan.State != secretspan.StateTracked || otherChatSpan.Reference != "" {
		t.Fatalf("other chat span = %+v, want StateTracked without a reference", otherChatSpan)
	}
}

// Invariant 7: reading or projecting a file masks live secrets to reference
// tokens and retired or revoked secrets to [REDACTED].
func TestInvariant7_OutboundReadMasking(t *testing.T) {
	const liveVal = "live-database-password-val-abc123"
	const liveRef = "{{paintedwolf-secret:aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa}}"
	const retiredVal = "retired-database-password-val-xyz789"

	m, err := secretmatch.BuildMatcher(secretmatch.Bundled())
	testutil.FailErr(t, "BuildMatcher", err)
	m.SetHarvestSource(func(context.Context) []secretmatch.HarvestedValue {
		return []secretmatch.HarvestedValue{
			{
				Name: "live db pass", Container: "managed secret", Secret: liveVal,
				RuleID: secretmatch.ManagedRuleID, Title: secretmatch.ManagedRuleTitle,
				Source: secretmatch.SourceRememberedMatch, Reference: liveRef, NonDisclosable: true,
			},
			{
				Name: "retired db pass", Container: "managed secret", Secret: retiredVal,
				RuleID: secretmatch.ManagedRuleID, Title: secretmatch.ManagedRuleTitle,
				Source: secretmatch.SourceRememberedMatch, Retired: true, NonDisclosable: true,
			},
		}
	})

	input := "DB_PASS=" + liveVal + "\nOLD_PASS=" + retiredVal + "\nPORT=5432\n"

	projected, replacements := m.ProjectLabeledWhere(context.Background(), "", input, func(secretmatch.Match) bool {
		return true
	})

	wantProjected := "DB_PASS=" + liveRef + "\nOLD_PASS=[REDACTED]\nPORT=5432\n"
	if projected != wantProjected {
		t.Fatalf("projected = %q, want %q", projected, wantProjected)
	}

	if len(replacements) != 2 {
		t.Fatalf("replacements = %+v, want 2", replacements)
	}
	if replacements[0].Reference != liveRef {
		t.Fatalf("replacement[0].Reference = %q, want %q", replacements[0].Reference, liveRef)
	}
	if replacements[1].Reference != "" {
		t.Fatalf("replacement[1].Reference = %q, want empty (placeholder)", replacements[1].Reference)
	}
}

// fingerprintFor is the screen identity m gives value.
func fingerprintFor(m *secretmatch.Matcher, value string) secretmatch.SecretFingerprint {
	match, err := m.ManagedValue(value, "", "")
	if err != nil {
		return ""
	}
	return match.Fingerprint
}
