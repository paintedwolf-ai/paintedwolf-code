package discovery

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"

	"github.com/lycaon/lycaon/internal/httpclient"
	"github.com/lycaon/lycaon/internal/llm/modelinfo"
	"github.com/lycaon/lycaon/internal/llm/providerauth"
	"github.com/lycaon/lycaon/internal/llm/providerhttp"
)

// VertexTimeout bounds an ADC token mint plus Model Garden pagination.
const VertexTimeout = 8 * time.Second

// vertexModelGardenHost serves the global publisher catalog.
var vertexModelGardenHost = "https://aiplatform.googleapis.com"

// vertexDiscoverPublisher scopes discovery to the supported publisher.
const vertexDiscoverPublisher = "google"

const vertexDiscoverPageSize = 1000

type vertexPublisherModelListResponse struct {
	PublisherModels []vertexPublisherModelRecord `json:"publisherModels"`
	NextPageToken   string                       `json:"nextPageToken"`
}

type vertexPublisherModelRecord struct {
	Name             string                        `json:"name"`
	SupportedActions *vertexSupportedActionsRecord `json:"supportedActions"`
}

type vertexSupportedActionsRecord struct {
	// OpenGenerationAiStudio marks generation-capable rows.
	OpenGenerationAiStudio json.RawMessage `json:"openGenerationAiStudio"`
}

// VertexModels lists generation-capable publisher models.
func VertexModels(ctx context.Context, client *http.Client) ([]modelinfo.Entry, error) {
	token, err := providerauth.VertexToken(ctx)
	if err != nil {
		return nil, err
	}
	if token == "" {
		return nil, fmt.Errorf("vertex: application default credentials returned an empty access token")
	}
	return discoverVertexModelsWithToken(ctx, client, token)
}

func discoverVertexModelsWithToken(ctx context.Context, client *http.Client, token string) ([]modelinfo.Entry, error) {
	if client == nil {
		client = providerhttp.DiscoveryClient(httpclient.DiscoveryTimeout)
	}
	var (
		out       []modelinfo.Entry
		seen      = make(map[string]struct{})
		pageToken string
	)
	for {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		page, next, err := fetchVertexPublisherModelsPage(ctx, client, token, pageToken)
		if err != nil {
			return nil, err
		}
		for _, rec := range page {
			if rec.SupportedActions == nil || rec.SupportedActions.OpenGenerationAiStudio == nil {
				continue
			}
			id := vertexModelIDFromName(rec.Name)
			if id == "" {
				continue
			}
			if _, dup := seen[id]; dup {
				continue
			}
			seen[id] = struct{}{}
			out = append(out, modelinfo.Entry{ID: id})
		}
		if next == "" {
			break
		}
		pageToken = next
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}

func fetchVertexPublisherModelsPage(ctx context.Context, client *http.Client, token, pageToken string) ([]vertexPublisherModelRecord, string, error) {
	u, err := url.Parse(vertexModelGardenHost + "/v1beta1/publishers/" + vertexDiscoverPublisher + "/models")
	if err != nil {
		return nil, "", err
	}
	q := u.Query()
	q.Set("view", "PUBLISHER_MODEL_VIEW_FULL")
	q.Set("pageSize", fmt.Sprintf("%d", vertexDiscoverPageSize))
	if pageToken != "" {
		q.Set("pageToken", pageToken)
	}
	u.RawQuery = q.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, "", err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := client.Do(req)
	if err != nil {
		return nil, "", err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return nil, "", fmt.Errorf("publisher models list HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	var listed vertexPublisherModelListResponse
	if err := providerhttp.DecodeDiscoveryResponse("vertex publisher models", resp, &listed); err != nil {
		return nil, "", err
	}
	return listed.PublisherModels, strings.TrimSpace(listed.NextPageToken), nil
}

// vertexModelIDFromName removes the transport's models path segment.
func vertexModelIDFromName(name string) string {
	name = strings.TrimSpace(name)
	const prefix = "publishers/"
	if !strings.HasPrefix(name, prefix) {
		return ""
	}
	rest := strings.TrimPrefix(name, prefix)
	publisher, modelPart, ok := strings.Cut(rest, "/models/")
	if !ok || publisher == "" || modelPart == "" {
		return ""
	}
	return publisher + "/" + modelPart
}
