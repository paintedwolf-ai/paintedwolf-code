package tools_test

import (
	"testing"

	"github.com/lycaon/lycaon/internal/protectedpath"
	"github.com/lycaon/lycaon/internal/tools"
)

func TestCredentialFilesAreSensitive(t *testing.T) {
	secrets := []string{
		".env",
		".env.production",
		"certs/server.pem",
		"deploy/id_rsa",
		"deploy/id_ed25519",
		".ssh/known_hosts",
		".aws/credentials",
		".kube/config",
		".config/gcloud/credentials.db",
		".azure/accessTokens.json",
		".docker/config.json",
		".npmrc",
		".pypirc",
		".netrc",
	}
	for _, rel := range secrets {
		if !protectedpath.IsCredentialFile(rel) {
			t.Errorf("IsCredentialFile(%q) = false, want true", rel)
		}
		if !tools.IsSensitivePath(rel) {
			t.Errorf("IsSensitivePath(%q) = false, want true (read tools route credentials through read)", rel)
		}
	}
}

func TestIsSensitivePathIncludesGitButCredentialFilesDoNot(t *testing.T) {
	if !tools.IsSensitivePath(".git/config") {
		t.Fatal("IsSensitivePath(.git/config) = false, want true")
	}
	if protectedpath.IsCredentialFile(".git/config") {
		t.Fatal("IsCredentialFile(.git/config) = true, want false — git is repository metadata")
	}
}

func TestOrdinaryPathsAreNotSecret(t *testing.T) {
	for _, rel := range []string{"README.md", "src/main.go", "config/settings.yaml"} {
		if protectedpath.IsCredentialFile(rel) {
			t.Errorf("IsCredentialFile(%q) = true, want false", rel)
		}
	}
}

// Classifying a published template as credential material harvests its sample
// values and blanks them out of unrelated prose and source.
func TestCredentialTemplatesAreNeitherCredentialsNorSensitive(t *testing.T) {
	for _, rel := range []string{
		".env.example",
		".env.sample",
		".env.template",
		".env.dist",
		"deploy/.env.example",
		"config/.env.example.local",
		"certs/server.pem.example",
	} {
		if !protectedpath.IsCredentialTemplate(rel) {
			t.Errorf("IsCredentialTemplate(%q) = false, want true", rel)
		}
		if protectedpath.IsCredentialFile(rel) {
			t.Errorf("IsCredentialFile(%q) = true, want false — a template holds no credential", rel)
		}
		if tools.IsSensitivePath(rel) {
			t.Errorf("IsSensitivePath(%q) = true, want false — a template is ordinary source", rel)
		}
	}
}

func TestRealCredentialPathsAreNotMistakenForTemplates(t *testing.T) {
	for _, rel := range []string{".env", ".env.production", ".env.local", "certs/server.pem"} {
		if protectedpath.IsCredentialTemplate(rel) {
			t.Errorf("IsCredentialTemplate(%q) = true, want false", rel)
		}
		if !protectedpath.IsCredentialFile(rel) {
			t.Errorf("IsCredentialFile(%q) = false, want true", rel)
		}
	}
}
