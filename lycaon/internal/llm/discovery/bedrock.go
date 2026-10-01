package discovery

import (
	"context"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/bedrock"
	bedrocktypes "github.com/aws/aws-sdk-go-v2/service/bedrock/types"
	"github.com/lycaon/lycaon/internal/llm/modelinfo"
	"github.com/lycaon/lycaon/internal/llm/providerauth"
)

// BedrockTimeout bounds credential resolution, the (up to two)
// ListInferenceProfiles round trips, and the availability sweep.
const BedrockTimeout = 25 * time.Second

// bedrockAvailabilityConcurrency bounds the availability sweep, which runs once
// per distinct routed model rather than per profile.
const bedrockAvailabilityConcurrency = 8

// BedrockModels lists Converse-usable ids from ListInferenceProfiles.
// Foundation-model ids are not the source: many are invokable only through a
// profile that supplies cross-region routing.
//
// SYSTEM_DEFINED profiles are the primary listing; APPLICATION profiles merge in
// best-effort, so an account lacking list access to them keeps the rest. A
// stored API key authenticates as a bearer token, otherwise the SDK chain signs.
func BedrockModels(ctx context.Context, region, apiKey string) ([]modelinfo.Entry, error) {
	cfg, err := providerauth.LoadBedrockConfig(ctx, region)
	if err != nil {
		return nil, err
	}
	client := bedrock.NewFromConfig(cfg, providerauth.BedrockControlOptions(apiKey)...)
	return discoverBedrockModelsWithClient(ctx, client)
}

// discoverBedrockModelsWithClient is the client-accepting seam tests use to
// point ListInferenceProfiles at an httptest server instead of AWS.
func discoverBedrockModelsWithClient(ctx context.Context, client *bedrock.Client) ([]modelinfo.Entry, error) {
	profiles := make(map[string]bedrockProfile)
	system, err := listBedrockInferenceProfiles(ctx, client, bedrocktypes.InferenceProfileTypeSystemDefined)
	if err != nil {
		return nil, err
	}
	for _, p := range system {
		profiles[p.entry.ID] = p
	}
	if app, appErr := listBedrockInferenceProfiles(ctx, client, bedrocktypes.InferenceProfileTypeApplication); appErr == nil {
		for _, p := range app {
			if _, exists := profiles[p.entry.ID]; !exists {
				profiles[p.entry.ID] = p
			}
		}
	}

	routed := make([]string, 0, len(profiles))
	for _, p := range profiles {
		routed = append(routed, p.foundationModel)
	}
	availability := gradeBedrockAvailability(ctx, client, routed)

	out := make([]modelinfo.Entry, 0, len(profiles))
	for _, p := range profiles {
		entry := p.entry
		entry.Callable = availability.callability(p.foundationModel)
		out = append(out, entry)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}

// bedrockProfile pairs a profile with the model it routes to. The routed id is
// the availability key; Entry.PricedAs is set only when it differs from the
// profile id, so it cannot serve as one.
type bedrockProfile struct {
	entry           modelinfo.Entry
	foundationModel string
}

// bedrockAvailability grades what an account may invoke. ListInferenceProfiles
// returns every curated profile whether or not the account holds the model, so
// two further control-plane reads decide callability.
type bedrockAvailability struct {
	// retired names models ListFoundationModels no longer returns. Absence is
	// the retirement fact; LEGACY is not, since those serve until end of life.
	retired map[string]bool
	// unaccepted names models whose agreement this account has not accepted.
	// NOT_AVAILABLE means the model refuses; AVAILABLE proves nothing.
	unaccepted map[string]bool
}

// callability grades one routed model. Evidence only ever removes a pair, so an
// ungraded model stays assignable.
func (a bedrockAvailability) callability(foundationModel string) modelinfo.CapabilityEvidence {
	id := strings.TrimSpace(foundationModel)
	if id == "" {
		return modelinfo.CapabilityEvidence{}
	}
	if a.retired[id] {
		return modelinfo.Evidence(modelinfo.CapabilityUnsupported, bedrockRetiredSource)
	}
	if a.unaccepted[id] {
		return modelinfo.Evidence(modelinfo.CapabilityUnsupported, bedrockAgreementSource)
	}
	return modelinfo.CapabilityEvidence{}
}

const (
	bedrockRetiredSource   = "bedrock-foundation-model-listing"
	bedrockAgreementSource = "bedrock-model-agreement"
)

// gradeBedrockAvailability reads both facts per distinct routed model. Reads are
// best-effort: an identity whose policy omits them grades nothing.
func gradeBedrockAvailability(ctx context.Context, client *bedrock.Client, routed []string) bedrockAvailability {
	out := bedrockAvailability{
		retired:    make(map[string]bool),
		unaccepted: make(map[string]bool),
	}
	models := distinctFoundationModels(routed)
	if len(models) == 0 {
		return out
	}

	if listed, err := client.ListFoundationModels(ctx, &bedrock.ListFoundationModelsInput{}); err == nil {
		known := make(map[string]bool, len(listed.ModelSummaries))
		for _, summary := range listed.ModelSummaries {
			if id := strings.TrimSpace(aws.ToString(summary.ModelId)); id != "" {
				known[id] = true
			}
		}
		// An empty catalog is an unusable answer, not proof every model retired.
		if len(known) > 0 {
			for _, id := range models {
				if !known[id] {
					out.retired[id] = true
				}
			}
		}
	}

	var (
		mu sync.Mutex
		wg sync.WaitGroup
	)
	work := make(chan string)
	workers := min(bedrockAvailabilityConcurrency, len(models))
	for range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for id := range work {
				if !bedrockAgreementUnaccepted(ctx, client, id) {
					continue
				}
				mu.Lock()
				out.unaccepted[id] = true
				mu.Unlock()
			}
		}()
	}
	for _, id := range models {
		select {
		case work <- id:
		case <-ctx.Done():
		}
	}
	close(work)
	wg.Wait()
	return out
}

// bedrockAgreementUnaccepted reports the modeled AgreementAvailability status.
// Only an explicit NOT_AVAILABLE counts; an error, a missing field, or any other
// status leaves the model ungraded.
func bedrockAgreementUnaccepted(ctx context.Context, client *bedrock.Client, foundationModel string) bool {
	out, err := client.GetFoundationModelAvailability(ctx, &bedrock.GetFoundationModelAvailabilityInput{
		ModelId: aws.String(foundationModel),
	})
	if err != nil || out == nil || out.AgreementAvailability == nil {
		return false
	}
	return out.AgreementAvailability.Status == bedrocktypes.AgreementStatusNotAvailable
}

func distinctFoundationModels(routed []string) []string {
	seen := make(map[string]struct{}, len(routed))
	out := make([]string, 0, len(routed))
	for _, id := range routed {
		id = strings.TrimSpace(id)
		if id == "" {
			continue
		}
		if _, dup := seen[id]; dup {
			continue
		}
		seen[id] = struct{}{}
		out = append(out, id)
	}
	sort.Strings(out)
	return out
}

func listBedrockInferenceProfiles(ctx context.Context, client *bedrock.Client, profileType bedrocktypes.InferenceProfileType) ([]bedrockProfile, error) {
	var (
		out       []bedrockProfile
		pageToken *string
	)
	for {
		page, err := client.ListInferenceProfiles(ctx, &bedrock.ListInferenceProfilesInput{
			TypeEquals: profileType,
			NextToken:  pageToken,
		})
		if err != nil {
			return nil, err
		}
		for _, p := range page.InferenceProfileSummaries {
			if p.Status != bedrocktypes.InferenceProfileStatusActive {
				continue
			}
			id := strings.TrimSpace(aws.ToString(p.InferenceProfileId))
			if id == "" {
				continue
			}
			entry := modelinfo.Entry{ID: id}
			fm := bedrockFoundationModelID(p.Models)
			// Pricing feeds key on the foundation-model id, not the profile
			// id; the profile's routed model ARNs carry it.
			if fm != "" && fm != id {
				entry.PricedAs = fm
			}
			out = append(out, bedrockProfile{entry: entry, foundationModel: fm})
		}
		if page.NextToken == nil || strings.TrimSpace(*page.NextToken) == "" {
			break
		}
		pageToken = page.NextToken
	}
	return out, nil
}

// bedrockFoundationModelID returns the first parseable routed model id.
func bedrockFoundationModelID(models []bedrocktypes.InferenceProfileModel) string {
	const marker = "foundation-model/"
	for _, m := range models {
		arn := aws.ToString(m.ModelArn)
		i := strings.LastIndex(arn, marker)
		if i < 0 {
			continue
		}
		if id := strings.TrimSpace(arn[i+len(marker):]); id != "" {
			return id
		}
	}
	return ""
}
