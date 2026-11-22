package projecttest

import (
	"testing"

	"github.com/lycaon/lycaon/internal/project"
	"github.com/lycaon/lycaon/internal/testdbfixture"
)

func NewRegistry(t testing.TB) *project.SQLRegistry {
	t.Helper()
	return project.NewSQLRegistry(testdbfixture.Open(t, "projects.db"))
}
