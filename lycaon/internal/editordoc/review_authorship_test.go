package editordoc

import (
	"testing"
	"unicode/utf16"

	"github.com/google/uuid"
	"github.com/lycaon/lycaon/internal/sourcebranch"
	"github.com/lycaon/lycaon/internal/sourceledger"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestChatComparisonKeepsOtherChatsAndHumanEditsDistinct(t *testing.T) {
	f, ledger := newLedgerAgentFixture(t, map[string]string{"a.txt": "base\n"})
	document := f.open(t, "a.txt")
	first, err := f.service.ApplyAgentEdit(t.Context(), agentEdit(document, "first\nbase\n"))
	testutil.FailErr(t, "publish first chat", err)
	typed, err := f.service.ReplaceSnapshot(t.Context(), document.ID, f.project.ID, SnapshotReplacement{
		DocumentCommand: DocumentCommand{ClientID: "window", SessionID: "human-chat", Turn: 3,
			OperationID: uuid.NewString(), ExpectedRevision: first.Document.Revision},
		Content: "first typed\nbase\n", EOL: "lf"})
	testutil.FailErr(t, "type inside first chat's change", err)
	input := agentEdit(typed, "first typed\nbase\nsecond 🐺\n")
	input.SessionID, input.ToolCallID = "chat-2", "call-2"
	second, err := f.service.ApplyAgentEdit(t.Context(), input)
	testutil.FailErr(t, "publish second chat with human typing", err)
	for _, sessionID := range []string{"chat-1", "chat-2"} {
		comparison, err := ledger.CompareScope(t.Context(), f.project.ID, sourcebranch.Trunk,
			sourceledger.Baseline{Kind: sourceledger.BaselineSession, SessionID: sessionID}, document.FileID,
			sourceledger.ScopeComparisonOptions{UnmarkUserEdits: true})
		testutil.FailErr(t, "compare chat", err)
		if comparison.Attribution == nil || comparison.After.Content != second.Document.Draft || comparison.After.VersionID == "" || comparison.Before.VersionID == "" {
			t.Fatalf("comparison lost exact saved endpoints: %+v", comparison)
		}
		selected, hidden := "", ""
		text := utf16.Encode([]rune(comparison.After.Content))
		for _, run := range comparison.Attribution.After {
			content := string(utf16.Decode(text[run.Index : run.Index+run.Length]))
			if run.Selected {
				selected += content
			}
			if !run.Visible {
				hidden += content
			}
			for _, author := range run.Contributors {
				if run.Selected && author.Origin == "agent" && author.SessionID != sessionID {
					t.Fatalf("marked another chat's text: %+v", run)
				}
			}
		}
		want := "first\n"
		if sessionID == "chat-2" {
			want = "second 🐺\n"
		}
		if selected != want || hidden != " typed" {
			t.Fatalf("chat %s selected %q, hidden %q; want %q and only human typing hidden", sessionID, selected, hidden, want)
		}
	}
}

func TestSavedLineKeepsAllContributingChats(t *testing.T) {
	f, ledger := newLedgerAgentFixture(t, map[string]string{"a.txt": "heading 🐺\r\nbase\r\n"})
	document := f.open(t, "a.txt")
	first, err := f.service.ApplyAgentEdit(t.Context(), agentEdit(document, "heading 🐺\nbase first\n"))
	testutil.FailErr(t, "publish first author", err)
	typed, err := f.service.ReplaceSnapshot(t.Context(), document.ID, f.project.ID, SnapshotReplacement{
		DocumentCommand: DocumentCommand{ClientID: "window", SessionID: "human-chat", Turn: 3,
			OperationID: uuid.NewString(), ExpectedRevision: first.Document.Revision},
		Content: "heading 🐺\nbase first typed\n", EOL: "crlf"})
	testutil.FailErr(t, "type on shared line", err)
	input := agentEdit(typed, "heading 🐺\nbase first typed second\n")
	input.SessionID, input.ToolCallID = "chat-2", "call-2"
	_, err = f.service.ApplyAgentEdit(t.Context(), input)
	testutil.FailErr(t, "publish mixed authors", err)
	attribution, err := ledger.QueryAttribution(t.Context(), f.project.ID, sourcebranch.Trunk, f.rootID, "a.txt")
	testutil.FailErr(t, "query saved authors", err)
	versions, err := ledger.QueryFileVersions(t.Context(), f.project.ID, document.FileID, 10, 0)
	testutil.FailErr(t, "query mixed publication history", err)
	if len(versions.Versions) == 0 || len(versions.Versions[0].Contributors) != 2 {
		t.Fatalf("publication history lost the human and second chat: %+v", versions.Versions)
	}
	effects, err := ledger.QueryFileEffects(t.Context(), f.project.ID, document.FileID, 0, 0, 10)
	testutil.FailErr(t, "query tool provenance", err)
	if len(effects.Effects) == 0 || effects.Effects[0].ActorClassFor("chat-2") != sourceledger.ActorMixed || effects.Effects[0].AuthoredTurn() != 0 {
		t.Fatalf("agent tools classified the publisher instead of contributors: %+v", effects)
	}
	authors := map[string]bool{}
	for _, interval := range attribution.Intervals {
		if interval.StartLine != 2 || interval.EndLine != 2 {
			t.Fatalf("attributed nonexistent line: %+v", interval)
		}
		authors[string(interval.Origin)+":"+interval.SessionID] = true
	}
	for _, author := range []string{"agent:chat-1", "agent:chat-2", "user:human-chat"} {
		if !authors[author] {
			t.Fatalf("saved line lost %s: %+v", author, attribution)
		}
	}
}

func TestSavedLineListsEachAuthorOnce(t *testing.T) {
	f, ledger := newLedgerAgentFixture(t, map[string]string{"a.txt": "intro\nTyped by the owner.\n"})
	document := f.open(t, "a.txt")
	_, err := f.service.ApplyAgentEdit(t.Context(), agentEdit(document, "intro\nEdited by the agent.\n"))
	testutil.FailErr(t, "publish interleaved edit", err)
	attribution, err := ledger.QueryAttribution(t.Context(), f.project.ID, sourcebranch.Trunk, f.rootID, "a.txt")
	testutil.FailErr(t, "query saved authors", err)
	agentIntervals := 0
	for _, interval := range attribution.Intervals {
		if interval.Origin == api.SourceChangeOriginAgent {
			agentIntervals++
			if interval.StartLine != 2 || interval.EndLine != 2 {
				t.Fatalf("attributed the wrong line: %+v", interval)
			}
		}
	}
	if agentIntervals != 1 {
		t.Fatalf("agent intervals = %d, want 1: %+v", agentIntervals, attribution.Intervals)
	}
}

func TestOpeningSharedDocumentInheritsRecordedAuthorship(t *testing.T) {
	f, ledger := newLedgerAgentFixture(t, map[string]string{"a.txt": "prior agent\n"})
	err := ledger.Record(t.Context(), sourceledger.RecordInput{ProjectID: f.project.ID, RootID: f.rootID,
		Path: "a.txt", OperationID: uuid.NewString(), Op: api.SourceChangeOpCreate,
		Origin: api.SourceChangeOriginAgent, SessionID: "prior-chat", Turn: 1,
		After: []byte("prior agent\n")})
	testutil.FailErr(t, "record pre-document agent write", err)
	document := f.open(t, "a.txt")
	_, err = f.service.ApplyAgentEdit(t.Context(), agentEdit(document, "prior agent\nnew agent\n"))
	testutil.FailErr(t, "publish shared document", err)
	attribution, err := ledger.QueryAttribution(t.Context(), f.project.ID, sourcebranch.Trunk, f.rootID, "a.txt")
	testutil.FailErr(t, "read inherited authorship", err)
	for _, interval := range attribution.Intervals {
		if interval.StartLine == 1 && interval.SessionID == "prior-chat" {
			return
		}
	}
	t.Fatalf("opening the document lost existing provenance: %+v", attribution)
}

func TestComparisonAcrossSnapshotHistoryKeepsMixedPublicationAuthors(t *testing.T) {
	f, ledger := newLedgerAgentFixture(t, map[string]string{"a.txt": "base\n"})
	err := ledger.Record(t.Context(), sourceledger.RecordInput{ProjectID: f.project.ID, RootID: f.rootID,
		Path: "a.txt", OperationID: uuid.NewString(), Op: api.SourceChangeOpCreate,
		Origin: api.SourceChangeOriginUser, After: []byte("base\n")})
	testutil.FailErr(t, "record initial snapshot", err)
	document := f.open(t, "a.txt")
	typed, err := f.service.ReplaceSnapshot(t.Context(), document.ID, f.project.ID, SnapshotReplacement{
		DocumentCommand: DocumentCommand{ClientID: "window", OperationID: uuid.NewString(), ExpectedRevision: document.Revision},
		Content:         "base typed\n", EOL: "lf"})
	testutil.FailErr(t, "type human text", err)
	_, err = f.service.ApplyAgentEdit(t.Context(), agentEdit(typed, "base typed agent\n"))
	testutil.FailErr(t, "publish mixed authors", err)
	comparison, err := ledger.CompareScope(t.Context(), f.project.ID, sourcebranch.Trunk,
		sourceledger.Baseline{}, document.FileID, sourceledger.ScopeComparisonOptions{UnmarkUserEdits: true})
	testutil.FailErr(t, "compare across initial snapshot", err)
	if comparison.Attribution == nil || comparison.Before.State != "absent" {
		t.Fatalf("expected comparison across snapshot boundary: %+v", comparison)
	}
	visible := ""
	text := utf16.Encode([]rune(comparison.After.Content))
	for _, run := range comparison.Attribution.After {
		if run.Visible {
			visible += string(utf16.Decode(text[run.Index : run.Index+run.Length]))
		}
	}
	if visible != " agent" {
		t.Fatalf("snapshot replay misattributed mixed publication: %q", visible)
	}
}
