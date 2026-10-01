package editordoc

import (
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/textfile"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestMutationRequiresExplicitOrigin(t *testing.T) {
	f := newExternalFixture(t, map[string]string{"a.txt": "base\n"})
	doc := f.open(t, "a.txt")
	for _, origin := range []api.SourceChangeOrigin{"", "unknown", api.SourceChangeOriginExternal, api.SourceChangeOriginUser, api.SourceChangeOriginAgent} {
		t.Run(string(origin), func(t *testing.T) {
			now := time.Now().UTC()
			m := &Mutation{
				ID: uuid.NewString(), DocumentID: doc.ID, ProjectID: doc.ProjectID,
				FileID: doc.FileID, RootID: doc.RootID, Path: doc.Path,
				ExpectedSHA256: doc.BaseSHA256, AfterSHA256: textfile.SHA256([]byte("draft\n")),
				Encoding: doc.Encoding, Content: "draft\n", DraftRevision: doc.Revision, EOL: doc.EOL,
				BeforeBytes: []byte("base\n"), AfterBytes: []byte("draft\n"),
				Status: "prepared", Origin: origin, CreatedAt: now, UpdatedAt: now,
			}
			if origin == api.SourceChangeOriginUser {
				owner, err := f.store.people.HostOwner(t.Context())
				testutil.FailErr(t, "read host owner", err)
				m.PersonID = owner.ID
			}
			err := f.store.InsertMutation(t.Context(), m)
			if origin != api.SourceChangeOriginUser && origin != api.SourceChangeOriginAgent {
				if err == nil {
					t.Fatalf("mutation accepted origin %q", origin)
				}
				if _, err := f.store.Mutation(t.Context(), m.ID); !errors.Is(err, ErrNotFound) {
					t.Fatalf("rejected mutation persisted: %v", err)
				}
				return
			}
			testutil.FailErr(t, "insert attributed mutation", err)
			stored, err := f.store.Mutation(t.Context(), m.ID)
			testutil.FailErr(t, "read attributed mutation", err)
			if stored.Origin != origin {
				t.Fatalf("stored origin = %q, want %q", stored.Origin, origin)
			}
		})
	}
}

func TestMutationOriginIsRequiredAtDatabaseBoundary(t *testing.T) {
	f := newExternalFixture(t, map[string]string{"a.txt": "base\n"})
	doc := f.open(t, "a.txt")
	for _, includeOrigin := range []bool{false, true} {
		id := uuid.NewString()
		columns, values := "", ""
		if includeOrigin {
			columns, values = ", origin, person_id", ", 'user', (SELECT id FROM people WHERE role = 'owner')"
		}
		_, err := f.store.db.ExecContext(t.Context(), `INSERT INTO editor_mutations (
			id, input_digest, document_id, project_id, branch_id, file_id, root_id, path,
			expected_sha256, after_sha256, encoding, content, draft_revision, eol,
			status, created_at, updated_at`+columns+`)
			SELECT ?, '', id, project_id, branch_id, file_id, root_id, path,
			base_sha256, base_sha256, encoding, draft, revision, eol,
			'prepared', created_at, updated_at`+values+`
			FROM editor_documents WHERE id = ?`, id, doc.ID)
		if !includeOrigin {
			if err == nil {
				t.Fatal("database accepted an omitted mutation origin")
			}
			if _, err := f.store.Mutation(t.Context(), id); !errors.Is(err, ErrNotFound) {
				t.Fatalf("unattributed mutation persisted: %v", err)
			}
			continue
		}
		testutil.FailErr(t, "insert explicit origin", err)
		stored, err := f.store.Mutation(t.Context(), id)
		testutil.FailErr(t, "read explicit origin", err)
		if stored.Origin != api.SourceChangeOriginUser {
			t.Fatalf("stored origin = %q, want user", stored.Origin)
		}
	}
}
