package store

import (
	"context"
	"database/sql"
	"errors"
	"github.com/lycaon/lycaon/internal/testdbfixture"
	"path/filepath"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/lycaon/lycaon/internal/db"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestSessionStoresRequireCompleteModelRef(t *testing.T) {
	dir := t.TempDir()
	sqlDB := testdbfixture.OpenPath(t, filepath.Join(dir, "store.db"))
	testdbseed.InsertProjectRoot(t, sqlDB, testdbseed.DefaultProjectID, dir)

	stores := map[string]interface {
		Create(context.Context, api.CreateSessionRequest, string) (*api.Session, error)
	}{
		"memory": NewMemory(),
		"sql":    NewSQL(sqlDB),
	}
	requests := map[string]api.CreateSessionRequest{
		"provider only": {ProviderID: "provider"},
		"model only":    {Model: "model"},
	}
	for storeName, sessionStore := range stores {
		for requestName, req := range requests {
			t.Run(storeName+"/"+requestName, func(t *testing.T) {
				_, err := sessionStore.Create(t.Context(), req, testdbseed.DefaultProjectID)
				if !errors.Is(err, ErrSessionModelRefIncomplete) {
					t.Fatalf("Create error = %v, want %v", err, ErrSessionModelRefIncomplete)
				}
			})
		}
	}
}

func TestSessionSchemaRequiresCompleteModelRef(t *testing.T) {
	dir := t.TempDir()
	sqlDB := testdbfixture.OpenPath(t, filepath.Join(dir, "store.db"))
	testdbseed.InsertProject(t, sqlDB, testdbseed.DefaultProjectID)

	now := time.Now().UTC().Format(time.RFC3339Nano)
	refs := []struct {
		providerID sql.NullString
		model      sql.NullString
	}{
		{providerID: db.NullString("provider")},
		{model: db.NullString("model")},
	}
	for _, ref := range refs {
		_, err := sqlDB.ExecContext(t.Context(), `
			INSERT INTO sessions (
				id, project_id, owner_person_id, posture, provider_id, model, status, created_at, activity_at, updated_at
			) VALUES (?, ?, (SELECT id FROM people WHERE role = 'owner'), 'build', ?, ?, 'idle', ?, ?, ?)
		`, uuid.NewString(), testdbseed.DefaultProjectID, ref.providerID, ref.model, now, now, now)
		if err == nil {
			t.Fatal("incomplete model reference was inserted")
		}
	}
}
