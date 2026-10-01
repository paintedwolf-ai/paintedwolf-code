package ollama

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/lycaon/lycaon/internal/llm/discovery"
	"github.com/lycaon/lycaon/internal/llm/modelinfo"
	"github.com/lycaon/lycaon/internal/llm/providerhttp"
	"github.com/lycaon/lycaon/internal/llm/providerwire"
)

// ollamaResidencyTimeout bounds the /api/ps read that precedes every request;
// the listing is in-memory on the server, so a slow answer means skip it.
const ollamaResidencyTimeout = time.Second

const (
	// Context headroom pads approximate prompt counts.
	ollamaContextHeadroom = 1024
	// The default reserve applies when output capacity is unknown.
	ollamaDefaultCompletionReserve = 4096
)

// Context buckets keep sizing stable across adjacent turns.
var ollamaContextBuckets = []int{8192, 16384, 32768, 65536, 131072, 262144}

// contextNeed is the context a request occupies, including headroom.
func contextNeed(promptTokens, completionReserve int) int {
	return promptTokens + completionReserve + ollamaContextHeadroom
}

// sizeNumCtx selects a context bucket and reports whether the prompt fits.
func sizeNumCtx(promptTokens, completionReserve, maxContext int) (numCtx int, fits bool) {
	need := contextNeed(promptTokens, completionReserve)
	numCtx = ollamaContextBuckets[0]
	for _, b := range ollamaContextBuckets {
		if b >= need {
			numCtx = b
			break
		}
		numCtx = b
	}
	fits = true
	if maxContext > 0 {
		fits = need <= maxContext
		if numCtx > maxContext {
			numCtx = maxContext
		}
	}
	return numCtx, fits
}

// preferResidentContext keeps a loaded runner's context when it holds the
// request: Ollama reloads the model whenever num_ctx differs from the runner's.
func preferResidentContext(bucket, need, resident, maxContext int) int {
	if resident < need || (maxContext > 0 && resident > maxContext) {
		return bucket
	}
	return resident
}

// ContextLimit returns the catalog or probed context limit.
func (p *Provider) ContextLimit(ctx context.Context, model string) int {
	if entry, ok := p.ModelEntry(model); ok && entry.ContextLength > 0 {
		return entry.ContextLength
	}
	p.capMu.Lock()
	if v, ok := p.capCache[model]; ok {
		p.capMu.Unlock()
		return v
	}
	p.capMu.Unlock()

	v := p.fetchModelContextLength(ctx, model)

	p.capMu.Lock()
	if p.capCache == nil {
		p.capCache = make(map[string]int)
	}
	p.capCache[model] = v
	p.capMu.Unlock()
	return v
}

// fetchModelContextLength reads the architecture-specific context limit.
func (p *Provider) fetchModelContextLength(ctx context.Context, model string) int {
	probeCtx, cancel := providerwire.ProbeContext(ctx, ollamaProbeTimeout)
	defer cancel()

	body, err := json.Marshal(map[string]string{"model": model})
	if err != nil {
		return 0
	}
	req, err := http.NewRequestWithContext(probeCtx, http.MethodPost, p.nativeBase+"/api/show", strings.NewReader(string(body)))
	if err != nil {
		return 0
	}
	req.Header.Set("Content-Type", "application/json")
	if p.apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+p.apiKey)
	}
	resp, err := p.streamClient.Do(req)
	if err != nil {
		return 0
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return 0
	}
	var show discovery.OllamaShowResponse
	if err := providerhttp.DecodeDiscoveryResponse("ollama context", resp, &show); err != nil {
		return 0
	}
	for key, raw := range show.ModelInfo {
		if !strings.HasSuffix(key, ".context_length") {
			continue
		}
		var n int
		if err := json.Unmarshal(raw, &n); err == nil && n > 0 {
			return n
		}
	}
	return 0
}

// ollamaPsResponse is the /api/ps listing of loaded runners. context_length
// is the num_ctx the runner was loaded with.
type ollamaPsResponse struct {
	Models []struct {
		Name          string `json:"name"`
		Model         string `json:"model"`
		ContextLength int    `json:"context_length"`
	} `json:"models"`
}

// residentContext returns the context length of the runner currently loaded
// for model, or 0 when none is loaded or the server cannot say. It is read
// per request because Ollama loads and evicts runners on its own schedule.
func (p *Provider) residentContext(ctx context.Context, model string) int {
	contextLength, _, _ := p.residency(ctx, model)
	return contextLength
}

// ModelResident reports whether a runner holds model, and with it the KV
// state of the prompt prefix it last served. known is false when the server
// cannot say.
func (p *Provider) ModelResident(ctx context.Context, model string) (resident, known bool) {
	_, resident, known = p.residency(ctx, model)
	return resident, known
}

// residency reads /api/ps for model's runner.
func (p *Provider) residency(ctx context.Context, model string) (contextLength int, resident, known bool) {
	probeCtx, cancel := providerwire.ProbeContext(ctx, ollamaResidencyTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(probeCtx, http.MethodGet, p.nativeBase+"/api/ps", nil)
	if err != nil {
		return 0, false, false
	}
	if p.apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+p.apiKey)
	}
	resp, err := p.streamClient.Do(req)
	if err != nil {
		return 0, false, false
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return 0, false, false
	}
	var ps ollamaPsResponse
	if err := providerhttp.DecodeDiscoveryResponse("ollama residency", resp, &ps); err != nil {
		return 0, false, false
	}
	for _, loaded := range ps.Models {
		if modelinfo.EquivalentID("ollama", loaded.Name, model) || modelinfo.EquivalentID("ollama", loaded.Model, model) {
			return loaded.ContextLength, true, true
		}
	}
	return 0, false, true
}
