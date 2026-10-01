package project_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/project"
)

func TestNormalizeRootDisplayLabel(t *testing.T) {
	t.Parallel()
	cases := []struct {
		in      string
		want    string
		wantErr bool
	}{
		{in: "  backend  ", want: "backend"},
		{in: "", wantErr: true},
		{in: "   ", wantErr: true},
		{in: "a\nb", wantErr: true},
		{in: "a\tb", wantErr: true},
		{in: strings.Repeat("x", project.MaxRootDisplayLabelRunes+1), wantErr: true},
		{in: strings.Repeat("x", project.MaxRootDisplayLabelRunes), want: strings.Repeat("x", project.MaxRootDisplayLabelRunes)},
		{in: "scratchpad", want: "scratchpad"},
	}
	for _, tc := range cases {
		got, err := project.NormalizeRootDisplayLabel(tc.in)
		if tc.wantErr {
			if err == nil {
				t.Fatalf("NormalizeRootDisplayLabel(%q) err = nil, want error", tc.in)
			}
			continue
		}
		if err != nil {
			t.Fatalf("NormalizeRootDisplayLabel(%q) unexpected err: %v", tc.in, err)
		}
		if got != tc.want {
			t.Fatalf("NormalizeRootDisplayLabel(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestNormalizeRootDisplayLabelRefusesHostNamespaces(t *testing.T) {
	t.Parallel()
	for _, label := range []string{"scratch", "SCRATCH", "  Scratch  "} {
		_, err := project.NormalizeRootDisplayLabel(label)
		if !errors.Is(err, project.ErrReservedRootLabel) || !errors.Is(err, project.ErrInvalidRootLabel) {
			t.Fatalf("NormalizeRootDisplayLabel(%q) err = %v, want a reserved invalid label", label, err)
		}
	}
}

func TestValidateLifecycleRefusesStoredHostNamespaceLabel(t *testing.T) {
	t.Parallel()
	p := &project.Project{
		ID: "project",
		Roots: []project.Root{{
			ID:        "root",
			ProjectID: "project",
			Path:      "/tmp/scratch",
			Label:     "scratch",
			IsPrimary: true,
			Kind:      project.RootKindAttached,
		}},
	}
	if err := project.ValidateLifecycle(p); err == nil {
		t.Fatal("ValidateLifecycle accepted a stored root labeled scratch")
	}
}

func TestValidateLifecycleRejectsMissingOrNonCanonicalRootLabels(t *testing.T) {
	t.Parallel()
	for _, label := range []string{"", " padded ", "@root", strings.Repeat("x", project.MaxRootDisplayLabelRunes+1)} {
		p := &project.Project{
			ID: "project",
			Roots: []project.Root{{
				ID:        "root",
				ProjectID: "project",
				Path:      "/tmp/root",
				Label:     label,
				IsPrimary: true,
				Kind:      project.RootKindAttached,
			}},
		}
		if err := project.ValidateLifecycle(p); err == nil {
			t.Fatalf("ValidateLifecycle label %q err = nil, want error", label)
		}
	}
}
