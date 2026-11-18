package project

import (
	"testing"

	"github.com/lycaon/lycaon/internal/testdbfixture"
)

func newProjectRegistry(t testing.TB) *SQLRegistry {
	t.Helper()
	return NewSQLRegistry(testdbfixture.Open(t, "projects.db"))
}
