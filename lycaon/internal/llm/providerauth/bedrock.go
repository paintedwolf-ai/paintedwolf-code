// Package providerauth resolves provider identities and authentication options shared by
// control-plane discovery and completion transports.
package providerauth

import (
	"context"
	"fmt"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/bedrock"
	"github.com/aws/aws-sdk-go-v2/service/bedrockruntime"
	"github.com/aws/smithy-go/auth/bearer"
)

// LoadBedrockConfig resolves ambient client configuration.
func LoadBedrockConfig(ctx context.Context, region string) (aws.Config, error) {
	opts := []func(*awsconfig.LoadOptions) error{}
	if strings.TrimSpace(region) != "" {
		opts = append(opts, awsconfig.WithRegion(region))
	}
	cfg, err := awsconfig.LoadDefaultConfig(ctx, opts...)
	if err != nil {
		return aws.Config{}, fmt.Errorf("bedrock: load aws config: %w", err)
	}
	return cfg, nil
}

func BedrockReadiness(ctx context.Context, region, apiKey string) (configuration, authentication string) {
	cfg, err := LoadBedrockConfig(ctx, region)
	if err != nil || !ValidRegionIdentifier(cfg.Region) {
		return "invalid", "missing"
	}
	if strings.TrimSpace(apiKey) != "" {
		return "valid", "unverified"
	}
	if cfg.Credentials == nil {
		return "valid", "missing"
	}
	credentials, err := cfg.Credentials.Retrieve(ctx)
	if err != nil || strings.TrimSpace(credentials.AccessKeyID) == "" {
		return "valid", "missing"
	}
	return "valid", "unverified"
}

// bedrockBearerSchemePreference gives explicit bearer keys precedence.
var bedrockBearerSchemePreference = []string{"httpBearerAuth"}

// BedrockRuntimeOptions configures explicit bearer authentication.
func BedrockRuntimeOptions(apiKey string) []func(*bedrockruntime.Options) {
	apiKey = strings.TrimSpace(apiKey)
	if apiKey == "" {
		return nil
	}
	return []func(*bedrockruntime.Options){func(o *bedrockruntime.Options) {
		o.BearerAuthTokenProvider = bearer.StaticTokenProvider{Token: bearer.Token{Value: apiKey}}
		o.AuthSchemePreference = bedrockBearerSchemePreference
	}}
}

// BedrockControlOptions configures discovery authentication.
func BedrockControlOptions(apiKey string) []func(*bedrock.Options) {
	apiKey = strings.TrimSpace(apiKey)
	if apiKey == "" {
		return nil
	}
	return []func(*bedrock.Options){func(o *bedrock.Options) {
		o.BearerAuthTokenProvider = bearer.StaticTokenProvider{Token: bearer.Token{Value: apiKey}}
		o.AuthSchemePreference = bedrockBearerSchemePreference
	}}
}
