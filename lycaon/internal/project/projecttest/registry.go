package projecttest

import (
	"context"
	"testing"

	"github.com/lycaon/lycaon/internal/project"
	"github.com/lycaon/lycaon/internal/testdbfixture"
)

func NewRegistry(t testing.TB) *project.SQLRegistry {
	t.Helper()
	return project.NewSQLRegistry(testdbfixture.Open(t, "projects.db"))
}

func CreateWithRoot(ctx context.Context, reg project.Registry, path string) (*project.Project, error) {
	return reg.Create(ctx, project.CreateParams{
		Roots: []project.AttachRootParams{{Path: path}},
	})
}
