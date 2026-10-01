package editordoc

import (
	"database/sql"
	"errors"
	"testing"

	"github.com/google/uuid"
	"github.com/lycaon/lycaon/internal/documentcore"
	"github.com/lycaon/lycaon/internal/people"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestTypedTextNamesThePersonAndTheirClientSeparately(t *testing.T) {
	f := newExternalFixture(t, map[string]string{"a.txt": "base"})
	d := f.open(t, "a.txt")
	owner, err := f.store.people.HostOwner(t.Context())
	testutil.FailErr(t, "read host owner", err)
	peer, joined := replicaForTest(t, f, d, "window:main")
	if len(joined.Participants) == 0 || joined.Participants[0].PersonID != owner.ID {
		t.Fatalf("participants = %+v, want the owner present", joined.Participants)
	}
	in := peerEdit(t, peer, joined, "window:main", documentcore.Edit{Index: 4, Insert: "!"})
	_, err = f.service.SubmitReplica(t.Context(), f.project.ID, d.ID, in)
	testutil.FailErr(t, "submit typing", err)

	var person sql.NullString
	var client string
	testutil.FailErr(t, "read contribution", f.store.db.QueryRowContext(t.Context(),
		`SELECT person_id, client_id FROM source_text_contributions WHERE document_id = ? AND operation_id = ?`, d.ID, in.OperationID).Scan(&person, &client))
	if person.String != owner.ID || client != "window:main" {
		t.Fatalf("contribution = person %q client %q", person.String, client)
	}
	testutil.FailErr(t, "read update log", f.store.db.QueryRowContext(t.Context(),
		`SELECT person_id, client_id FROM editor_replica_updates WHERE document_id = ? AND operation_id = ?`, d.ID, in.OperationID).Scan(&person, &client))
	if person.String != owner.ID || client != "window:main" {
		t.Fatalf("update log = person %q client %q", person.String, client)
	}
}

func TestAClientBelongsToOnePerson(t *testing.T) {
	f := newExternalFixture(t, map[string]string{"a.txt": "base"})
	d := f.open(t, "a.txt")
	peer, joined := replicaForTest(t, f, d, "window:main")
	stranger := people.WithCaller(t.Context(), people.Person{ID: uuid.NewString(), Role: api.PersonRoleOwner})

	in := peerEdit(t, peer, joined, "window:main", documentcore.Edit{Index: 0, Insert: "x"})
	if _, err := f.service.SubmitReplica(stranger, f.project.ID, d.ID, in); !errors.Is(err, ErrReplicaIdentity) {
		t.Fatalf("another person's submission err = %v, want ErrReplicaIdentity", err)
	}
	if _, err := f.service.Join(stranger, d.ID, f.project.ID, ReplicaJoin{ClientID: "window:main", Incarnation: uuid.NewString(), Epoch: 1}); !errors.Is(err, ErrReplicaIdentity) {
		t.Fatalf("another person joining the client err = %v, want ErrReplicaIdentity", err)
	}
}
