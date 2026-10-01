package openaicompat

import (
	"context"
	"testing"

	"github.com/lycaon/lycaon/internal/llm/providerauth"
)

func TestVertexProviderBuildsRegionalEndpoint(t *testing.T) {
	t.Setenv("GOOGLE_CLOUD_PROJECT", "my-proj")
	t.Setenv("GOOGLE_CLOUD_REGION", "europe-west1")
	t.Setenv("CLOUD_ML_REGION", "")
	t.Setenv("GOOGLE_CLOUD_LOCATION", "")

	p, ok := NewVertex(context.Background(), "vertex", nil).(*Provider)
	if !ok {
		t.Fatalf("vertex provider type = %T, want *OpenAIProvider", p)
	}
	got := p.chatCompletionURL("google/gemini-2.5-pro")
	want := "https://europe-west1-aiplatform.googleapis.com/v1/projects/my-proj/locations/europe-west1/endpoints/openapi/chat/completions"
	if got != want {
		t.Fatalf("vertex url = %q, want %q", got, want)
	}
	if p.tokenSource == nil {
		t.Fatal("vertex must mint a per-request OAuth token, tokenSource is nil")
	}
}

func TestVertexDefaultsRegion(t *testing.T) {
	t.Setenv("GOOGLE_CLOUD_PROJECT", "p")
	t.Setenv("GOOGLE_CLOUD_REGION", "")
	t.Setenv("CLOUD_ML_REGION", "")
	t.Setenv("GOOGLE_CLOUD_LOCATION", "")

	p := NewVertex(context.Background(), "vertex", nil).(*Provider)
	if got := p.chatCompletionURL("google/gemini-2.5-pro"); got != "https://us-central1-aiplatform.googleapis.com/v1/projects/p/locations/us-central1/endpoints/openapi/chat/completions" {
		t.Fatalf("default-region url = %q", got)
	}
}

func TestVertexReadinessKeepsConfigurationAndAuthenticationIndependent(t *testing.T) {
	t.Setenv("GOOGLE_CLOUD_PROJECT", "configured-project")
	t.Setenv("GOOGLE_CLOUD_QUOTA_PROJECT", "")
	t.Setenv("GCLOUD_PROJECT", "")
	t.Setenv("GOOGLE_APPLICATION_CREDENTIALS", t.TempDir()+"/missing.json")
	configuration, authentication := providerauth.VertexReadiness(t.Context())
	if configuration != "valid" || authentication != "missing" {
		t.Fatalf("readiness = %s/%s, want valid/missing", configuration, authentication)
	}

	t.Setenv("GOOGLE_CLOUD_PROJECT", "")
	configuration, authentication = providerauth.VertexReadiness(t.Context())
	if configuration != "invalid" || authentication != "missing" {
		t.Fatalf("readiness without project = %s/%s, want invalid/missing", configuration, authentication)
	}

	t.Setenv("GOOGLE_CLOUD_QUOTA_PROJECT", "billing-project-only")
	configuration, authentication = providerauth.VertexReadiness(t.Context())
	if configuration != "invalid" || authentication != "missing" {
		t.Fatalf("quota-project-only readiness = %s/%s, want invalid/missing", configuration, authentication)
	}

	t.Setenv("GOOGLE_CLOUD_PROJECT", "configured-project")
	t.Setenv("GOOGLE_CLOUD_REGION", "INVALID/REGION")
	configuration, authentication = providerauth.VertexReadiness(t.Context())
	if configuration != "invalid" || authentication != "missing" {
		t.Fatalf("invalid-region readiness = %s/%s, want invalid/missing", configuration, authentication)
	}
}
