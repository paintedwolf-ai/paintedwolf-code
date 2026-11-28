package submissions

import (
	"context"
	"encoding/json"
	"go/ast"
	"go/parser"
	"go/token"
	"strconv"
	"testing"

	"github.com/google/uuid"
	"github.com/lycaon/lycaon/internal/people"
	"github.com/lycaon/lycaon/internal/promptresult"
	"github.com/lycaon/lycaon/internal/queue"
	"github.com/lycaon/lycaon/internal/session/lifecycle"
	"github.com/lycaon/lycaon/internal/session/promptinput"
	"github.com/lycaon/lycaon/internal/session/promptstate"
	"github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/internal/session/submissionstate"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestPromptAdmissionPreservesAuthorshipForEveryOrigin(t *testing.T) {
	ctx := t.Context()
	st := openCompletionStore(t)
	sess, err := st.Sessions.Create(ctx, api.CreateSessionRequest{}, testdbseed.DefaultProjectID)
	testutil.FailErr(t, "create session", err)
	owner, err := st.People.HostOwner(ctx)
	testutil.FailErr(t, "read host owner", err)

	for _, origin := range declaredSubmissionOrigins(t) {
		if origin == store.PromptSubmissionOriginUser {
			t.Run(string(origin), func(t *testing.T) {
				svc := newAuthorshipService(t, st, owner.ID)
				in := promptinput.Input{Text: "user prompt", AuthorPersonID: uuid.NewString()}
				row, _, err := svc.AdmitPrompt(people.WithCaller(ctx, owner), sess.ID, uuid.NewString(), in, in)
				testutil.FailErr(t, "admit user turn", err)
				_, err = svc.RunPromptSubmission(ctx, row.ID)
				testutil.FailErr(t, "run user receipt", err)
				assertAuthorshipReceipt(t, st, row.ID, origin, owner.ID, "")
			})
			continue
		}
		for _, visible := range []bool{false, true} {
			name := "internal"
			wantAuthor := ""
			if visible {
				name = "transcript-visible"
				wantAuthor = owner.ID
			}
			t.Run(string(origin)+"/"+name, func(t *testing.T) {
				svc := newAuthorshipService(t, st, wantAuthor)
				id := uuid.NewString()
				in := promptinput.Input{
					SubmissionID: id, Text: "host prompt", AuthorPersonID: wantAuthor,
					HostSignal: &promptinput.HostSignal{
						Kind: api.MessageKindHostNudge, ID: id,
						TranscriptVisible: visible, AuthorPersonID: wantAuthor,
					},
				}
				_, err := svc.HostTurnWithAdmission(ctx, sess.ID, origin, in, nil)
				testutil.FailErr(t, "admit and run host turn", err)
				assertAuthorshipReceipt(t, st, id, origin, "", wantAuthor)
			})
		}
	}
}

func newAuthorshipService(t *testing.T, st *store.Repositories, wantAuthor string) *Service {
	t.Helper()
	return New(st.Sessions, st.Submissions, st.People, queue.New(), lifecycle.New(st.Sessions),
		nil, nil, nil, &submissionstate.Service{}, &promptstate.MutexRegistry{},
		func(_ context.Context, _ string, in promptinput.Input, lock *promptstate.Lock) (*promptresult.Result, error) {
			defer lock.Unlock()
			if in.AuthorPersonID != wantAuthor {
				t.Errorf("replayed author = %q, want %q", in.AuthorPersonID, wantAuthor)
			}
			return &promptresult.Result{}, nil
		}, nil, func(context.Context, string) bool { return true },
		func(context.Context, string) bool { return false })
}

func assertAuthorshipReceipt(t *testing.T, st *store.Repositories, id string, origin store.PromptSubmissionOrigin, sender, hostAuthor string) {
	t.Helper()
	row, err := st.Submissions.GetPromptSubmission(t.Context(), id)
	testutil.FailErr(t, "read persisted receipt", err)
	if row.Origin != origin || row.SubmittedBy != sender || row.Status != store.PromptSubmissionComplete {
		t.Errorf("receipt origin=%q sender=%q status=%q, want %q %q complete", row.Origin, row.SubmittedBy, row.Status, origin, sender)
	}
	var in promptinput.Input
	testutil.FailErr(t, "decode receipt payload", json.Unmarshal([]byte(row.InputJSON), &in))
	var gotHostAuthor string
	if in.HostSignal != nil {
		gotHostAuthor = in.HostSignal.AuthorPersonID
	}
	if gotHostAuthor != hostAuthor {
		t.Errorf("stored host author = %q, want %q", gotHostAuthor, hostAuthor)
	}
}

// Discover from the typed origin constants adjacent to the receipt contract.
func declaredSubmissionOrigins(t *testing.T) []store.PromptSubmissionOrigin {
	t.Helper()
	file, err := parser.ParseFile(token.NewFileSet(), "../store/prompt_submission.go", nil, parser.SkipObjectResolution)
	testutil.FailErr(t, "parse prompt submission origin definitions", err)
	var origins []store.PromptSubmissionOrigin
	for node := range ast.Preorder(file) {
		spec, ok := node.(*ast.ValueSpec)
		if !ok {
			continue
		}
		kind, ok := spec.Type.(*ast.Ident)
		if !ok || kind.Name != "PromptSubmissionOrigin" {
			continue
		}
		for _, value := range spec.Values {
			literal, ok := value.(*ast.BasicLit)
			if !ok || literal.Kind != token.STRING {
				t.Fatalf("origin declaration uses an unsupported expression: %T", value)
			}
			origin, err := strconv.Unquote(literal.Value)
			testutil.FailErr(t, "read prompt submission origin", err)
			origins = append(origins, store.PromptSubmissionOrigin(origin))
		}
	}
	if len(origins) == 0 {
		t.Fatal("no prompt submission origins discovered")
	}
	return origins
}
