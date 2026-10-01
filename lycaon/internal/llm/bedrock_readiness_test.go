package llm

import (
	"context"
	"testing"

	"github.com/lycaon/lycaon/internal/llm/providerauth"
)

func TestBedrockReadinessRequiresRegionAndUsableAuthentication(t *testing.T) {
	configDir := t.TempDir()
	t.Setenv("AWS_CONFIG_FILE", configDir+"/config")
	t.Setenv("AWS_SHARED_CREDENTIALS_FILE", configDir+"/credentials")
	t.Setenv("AWS_EC2_METADATA_DISABLED", "true")
	t.Setenv("AWS_REGION", "us-west-2")
	t.Setenv("AWS_DEFAULT_REGION", "")
	t.Setenv("AWS_ACCESS_KEY_ID", "test-access-key")
	t.Setenv("AWS_SECRET_ACCESS_KEY", "test-secret-key")

	if configuration, authentication := providerauth.BedrockReadiness(context.Background(), "", ""); configuration != "valid" || authentication != "unverified" {
		t.Fatalf("ambient readiness = %s/%s, want valid/unverified", configuration, authentication)
	}
	if configuration, authentication := providerauth.BedrockReadiness(context.Background(), "us-east-1", "bedrock-api-key"); configuration != "valid" || authentication != "unverified" {
		t.Fatalf("stored-key readiness = %s/%s, want valid/unverified", configuration, authentication)
	}

	t.Setenv("AWS_REGION", "")
	t.Setenv("AWS_ACCESS_KEY_ID", "")
	t.Setenv("AWS_SECRET_ACCESS_KEY", "")
	if configuration, authentication := providerauth.BedrockReadiness(context.Background(), "", "bedrock-api-key"); configuration != "invalid" || authentication != "missing" {
		t.Fatalf("region-less readiness = %s/%s, want invalid/missing", configuration, authentication)
	}
}
