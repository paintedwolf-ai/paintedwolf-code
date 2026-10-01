package discovery

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/lycaon/lycaon/internal/httpclient"
	"github.com/lycaon/lycaon/internal/llm/modelinfo"
	"github.com/lycaon/lycaon/internal/llm/providerhttp"
)

// AzureManagementHost serves deployment discovery.
var AzureManagementHost = "https://management.azure.com"

// azureARMAPIVersion is the Cognitive Services deployments list API version.
const azureARMAPIVersion = "2023-05-01"

// azureAADAuthorityDefault is the public-cloud AAD token endpoint host.
const azureAADAuthorityDefault = "https://login.microsoftonline.com"

// AzureIMDSTokenURL mints managed-identity tokens.
var AzureIMDSTokenURL = "http://169.254.169.254/metadata/identity/oauth2/token" // #nosec G101 -- fixed IMDS link-local address, not a credential

// azureIMDSTimeout bounds the managed-identity probe.
const azureIMDSTimeout = 500 * time.Millisecond

type azureARMDeploymentListResponse struct {
	Value []azureARMDeploymentRecord `json:"value"`
}

type azureARMDeploymentRecord struct {
	Name       string `json:"name"`
	Properties struct {
		Model struct {
			Name string `json:"name"`
		} `json:"model"`
	} `json:"properties"`
}

// ProbeAzureDataPlane validates the chat credential.
func ProbeAzureDataPlane(ctx context.Context, baseURL, apiKey string, client *http.Client) error {
	baseURL = strings.TrimRight(strings.TrimSpace(baseURL), "/")
	if baseURL == "" {
		return fmt.Errorf("base_url required")
	}
	if client == nil {
		client = providerhttp.DiscoveryClient(httpclient.DiscoveryTimeout)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, baseURL+"/openai/v1/models", nil)
	if err != nil {
		return err
	}
	req.Header.Set("api-key", strings.TrimSpace(apiKey))
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return fmt.Errorf("azure models probe HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	return nil
}

// azureARMConfig identifies the account containing the deployments.
type azureARMConfig struct {
	subscriptionID string
	resourceGroup  string
}

func loadAzureARMConfigFromEnv() (azureARMConfig, bool) {
	cfg := azureARMConfig{
		subscriptionID: strings.TrimSpace(os.Getenv("AZURE_SUBSCRIPTION_ID")),
		resourceGroup:  strings.TrimSpace(os.Getenv("AZURE_RESOURCE_GROUP")),
	}
	return cfg, cfg.subscriptionID != "" && cfg.resourceGroup != ""
}

// azureAccountNameFromBaseURL extracts the account from its data-plane host.
func azureAccountNameFromBaseURL(baseURL string) (string, bool) {
	u, err := url.Parse(strings.TrimSpace(baseURL))
	if err != nil || u.Host == "" {
		return "", false
	}
	host := u.Hostname()
	const suffix = ".openai.azure.com"
	if !strings.HasSuffix(host, suffix) {
		return "", false
	}
	name := strings.TrimSuffix(host, suffix)
	if name == "" {
		return "", false
	}
	return name, true
}

// AzureModels lists deployments when management identity is configured.
func AzureModels(ctx context.Context, baseURL string, client *http.Client) ([]modelinfo.Entry, error) {
	account, ok := azureAccountNameFromBaseURL(baseURL)
	if !ok {
		return nil, nil
	}
	armCfg, ok := loadAzureARMConfigFromEnv()
	if !ok {
		return nil, nil
	}
	if client == nil {
		client = providerhttp.DiscoveryClient(httpclient.DiscoveryTimeout)
	}
	token, err := azureManagementAccessToken(ctx, client)
	if err != nil || token == "" {
		return nil, nil //nolint:nilerr // discovery is unavailable without a management token
	}
	return listAzureARMDeployments(ctx, client, armCfg, account, token)
}

func listAzureARMDeployments(ctx context.Context, client *http.Client, cfg azureARMConfig, account, token string) ([]modelinfo.Entry, error) {
	listURL := fmt.Sprintf(
		"%s/subscriptions/%s/resourceGroups/%s/providers/Microsoft.CognitiveServices/accounts/%s/deployments?api-version=%s",
		AzureManagementHost,
		url.PathEscape(cfg.subscriptionID),
		url.PathEscape(cfg.resourceGroup),
		url.PathEscape(account),
		url.QueryEscape(azureARMAPIVersion),
	)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, listURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode == http.StatusNotFound {
		return nil, nil
	}
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return nil, fmt.Errorf("arm deployments list HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	var listed azureARMDeploymentListResponse
	if err := providerhttp.DecodeDiscoveryResponse("azure deployments", resp, &listed); err != nil {
		return nil, err
	}
	out := make([]modelinfo.Entry, 0, len(listed.Value))
	seen := make(map[string]struct{}, len(listed.Value))
	for _, item := range listed.Value {
		id := strings.TrimSpace(item.Name)
		if id == "" {
			continue
		}
		if _, dup := seen[id]; dup {
			continue
		}
		seen[id] = struct{}{}
		entry := modelinfo.Entry{ID: id}
		// Pricing follows the underlying model rather than the deployment name.
		if m := strings.TrimSpace(item.Properties.Model.Name); m != "" && m != id {
			entry.PricedAs = m
		}
		out = append(out, entry)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}

// azureManagementAccessToken resolves a management-plane credential.
func azureManagementAccessToken(ctx context.Context, client *http.Client) (string, error) {
	if token, err := azureClientCredentialsToken(ctx, client); err == nil && token != "" {
		return token, nil
	}
	return azureManagedIdentityToken(ctx, client)
}

// azureClientCredentialsToken uses the configured service principal.
func azureClientCredentialsToken(ctx context.Context, client *http.Client) (string, error) {
	tenantID := strings.TrimSpace(os.Getenv("AZURE_TENANT_ID"))
	clientID := strings.TrimSpace(os.Getenv("AZURE_CLIENT_ID"))
	clientSecret := strings.TrimSpace(os.Getenv("AZURE_CLIENT_SECRET"))
	if tenantID == "" || clientID == "" || clientSecret == "" {
		return "", nil
	}
	authority := strings.TrimRight(strings.TrimSpace(os.Getenv("AZURE_AUTHORITY_HOST")), "/")
	if authority == "" {
		authority = azureAADAuthorityDefault
	}
	tokenURL := authority + "/" + tenantID + "/oauth2/v2.0/token"
	form := url.Values{
		"grant_type":    {"client_credentials"},
		"client_id":     {clientID},
		"client_secret": {clientSecret},
		"scope":         {AzureManagementHost + "/.default"},
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, tokenURL, strings.NewReader(form.Encode())) // #nosec G704 -- tokenURL is the operator's own AAD authority + tenant, from env vars
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	return doAzureTokenRequest(client, req)
}

// azureManagedIdentityToken uses the ambient compute identity.
func azureManagedIdentityToken(ctx context.Context, client *http.Client) (string, error) {
	u, err := url.Parse(AzureIMDSTokenURL)
	if err != nil {
		return "", err
	}
	q := u.Query()
	q.Set("api-version", "2019-08-01")
	q.Set("resource", AzureManagementHost+"/")
	if clientID := strings.TrimSpace(os.Getenv("AZURE_CLIENT_ID")); clientID != "" {
		q.Set("client_id", clientID)
	}
	u.RawQuery = q.Encode()

	imdsCtx, cancel := context.WithTimeout(ctx, azureIMDSTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(imdsCtx, http.MethodGet, u.String(), nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Metadata", "true")
	token, err := doAzureTokenRequest(client, req)
	if err != nil {
		return "", nil // unreachable off-Azure; not a discovery-blocking error
	}
	return token, nil
}

func doAzureTokenRequest(client *http.Client, req *http.Request) (string, error) {
	resp, err := client.Do(req) // #nosec G704 -- req targets AAD authority or IMDS, both fixed/operator-configured, never user input
	if err != nil {
		return "", err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return "", fmt.Errorf("azure AAD token HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	var payload struct {
		AccessToken string `json:"access_token"`
	}
	if err := providerhttp.DecodeCredentialResponse("azure oauth", resp, &payload); err != nil {
		return "", err
	}
	return strings.TrimSpace(payload.AccessToken), nil
}
