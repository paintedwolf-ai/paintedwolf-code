package providerauth

import (
	"context"
	"fmt"
	"os"
	"strings"
	"sync"

	"golang.org/x/oauth2"
	"golang.org/x/oauth2/google"
)

// VertexScope authorizes prediction calls.
const VertexScope = "https://www.googleapis.com/auth/cloud-platform"

// VertexDefaultRegion applies when configuration omits a region.
const VertexDefaultRegion = "us-central1"

func VertexProject(ctx context.Context) (string, error) {
	for _, env := range []string{"GOOGLE_CLOUD_PROJECT", "GCLOUD_PROJECT"} {
		if v := strings.TrimSpace(os.Getenv(env)); v != "" {
			return v, nil
		}
	}
	creds, err := google.FindDefaultCredentials(ctx, VertexScope)
	if err != nil {
		return "", fmt.Errorf("vertex: application default credentials: %w", err)
	}
	if project := strings.TrimSpace(creds.ProjectID); project != "" {
		return project, nil
	}
	return "", fmt.Errorf("vertex: Google Cloud project is not configured")
}

func VertexReadiness(ctx context.Context) (configuration, authentication string) {
	projectConfigured := false
	for _, env := range []string{"GOOGLE_CLOUD_PROJECT", "GCLOUD_PROJECT"} {
		if strings.TrimSpace(os.Getenv(env)) != "" {
			projectConfigured = true
			break
		}
	}
	creds, err := google.FindDefaultCredentials(ctx, VertexScope)
	if !projectConfigured && err == nil && creds != nil && strings.TrimSpace(creds.ProjectID) != "" {
		projectConfigured = true
	}
	configuration = "invalid"
	if projectConfigured && ValidRegionIdentifier(VertexRegion()) {
		configuration = "valid"
	}
	authentication = "missing"
	if err == nil && creds != nil && creds.TokenSource != nil {
		authentication = "unverified"
	}
	return configuration, authentication
}

func VertexRegion() string {
	for _, env := range []string{"GOOGLE_CLOUD_REGION", "CLOUD_ML_REGION", "GOOGLE_CLOUD_LOCATION"} {
		if v := strings.TrimSpace(os.Getenv(env)); v != "" {
			return v
		}
	}
	return VertexDefaultRegion
}

// VertexToken mints a token for one request.
func VertexToken(ctx context.Context) (string, error) {
	creds, err := google.FindDefaultCredentials(ctx, VertexScope)
	if err != nil {
		return "", fmt.Errorf("vertex: application default credentials: %w", err)
	}
	tok, err := creds.TokenSource.Token()
	if err != nil {
		return "", fmt.Errorf("vertex: oauth token: %w", err)
	}
	return tok.AccessToken, nil
}

// NewVertexTokenSource resolves and caches a refreshing token source.
func NewVertexTokenSource() func(context.Context) (string, error) {
	return newADCTokenSourceWithResolver(func(ctx context.Context) (oauth2.TokenSource, error) {
		creds, err := google.FindDefaultCredentials(ctx, VertexScope)
		if err != nil {
			return nil, fmt.Errorf("vertex: application default credentials: %w", err)
		}
		return creds.TokenSource, nil
	})
}

func newADCTokenSourceWithResolver(
	resolve func(context.Context) (oauth2.TokenSource, error),
) func(context.Context) (string, error) {
	var (
		mu  sync.Mutex
		src oauth2.TokenSource
	)
	return func(ctx context.Context) (string, error) {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		mu.Lock()
		if src == nil {
			resolved, err := resolve(ctx)
			if err != nil {
				mu.Unlock()
				return "", err
			}
			if resolved == nil {
				mu.Unlock()
				return "", fmt.Errorf("vertex: application default credentials returned no token source")
			}
			src = resolved
		}
		tokenSource := src
		mu.Unlock()
		tok, tokErr := tokenSource.Token()
		if tokErr != nil {
			return "", fmt.Errorf("vertex: oauth token: %w", tokErr)
		}
		return tok.AccessToken, nil
	}
}
