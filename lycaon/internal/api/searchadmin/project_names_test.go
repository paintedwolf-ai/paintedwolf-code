package searchadmin

import (
	"testing"

	"github.com/lycaon/lycaon/internal/project"
)

func TestSearchProjectNamesUsePrimaryRoot(t *testing.T) {
	p := project.Project{
		ID: "project",
		Roots: []project.Root{
			{Label: "secondary"},
			{Label: "primary", IsPrimary: true},
		},
	}
	if got := searchProjectSlug(p); got != "primary" {
		t.Fatalf("slug = %q, want primary", got)
	}
	if got := searchProjectDisplayName(&p); got != "primary" {
		t.Fatalf("display name = %q, want primary", got)
	}
}
