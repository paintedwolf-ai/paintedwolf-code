package security

import (
	"context"
	"errors"
	"testing"

	"github.com/lycaon/lycaon/internal/tools"
)

type fileEvidenceAdmission struct {
	project string
	err     error
}

func (*fileEvidenceAdmission) Start(string) {}
func (f *fileEvidenceAdmission) Await(_ context.Context, wait tools.CredentialWait) error {
	f.project = wait.ProjectID
	return f.err
}
func (*fileEvidenceAdmission) Delivered(context.Context, tools.CredentialFileRead) error { return nil }
func (*fileEvidenceAdmission) Authored(context.Context, tools.AuthoredCredentialValues) error {
	return nil
}

func TestProviderEvidenceWaitsForFileAdmission(t *testing.T) {
	unavailable := errors.New("file capture unavailable")
	admission := &fileEvidenceAdmission{err: unavailable}
	runtime := &Runtime{CredentialFiles: admission}
	if _, err := runtime.ModelScreeningValues(t.Context(), "project", "chat"); !errors.Is(err, unavailable) || admission.project != "project" {
		t.Fatalf("admission=%+v error=%v", admission, err)
	}
	admission.err = nil
	if values, err := runtime.ModelScreeningValues(t.Context(), "project", "chat"); err != nil || len(values) != 0 {
		t.Fatalf("empty managed evidence=%v error=%v", values, err)
	}
}

func (*fileEvidenceAdmission) ForgetProject(context.Context, string) error { return nil }
