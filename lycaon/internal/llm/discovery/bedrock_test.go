package discovery

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/bedrock"
	"github.com/lycaon/lycaon/internal/llm/modelinfo"
	"github.com/lycaon/lycaon/internal/llm/providerauth"
	"github.com/lycaon/lycaon/internal/testutil"
)

func testBedrockClient(t *testing.T, handler http.HandlerFunc) *bedrock.Client {
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	cfg := aws.Config{
		Region:      "us-east-1",
		Credentials: credentials.NewStaticCredentialsProvider("AKIATEST", "secret", ""),
	}
	return bedrock.NewFromConfig(cfg, func(o *bedrock.Options) {
		o.BaseEndpoint = aws.String(srv.URL)
	})
}

func TestDiscoverBedrockModelsMergesSystemAndApplicationProfiles(t *testing.T) {
	client := testBedrockClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/inference-profiles" {
			http.NotFound(w, r)
			return
		}
		switch r.URL.Query().Get("type") {
		case "SYSTEM_DEFINED":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"inferenceProfileSummaries": []map[string]any{
					{
						"inferenceProfileId":   "us.anthropic.claude-sonnet-4-5-20250929-v1:0",
						"inferenceProfileName": "US Claude Sonnet",
						"inferenceProfileArn":  "arn:aws:bedrock:us-east-1:111:inference-profile/us.anthropic.claude-sonnet-4-5-20250929-v1:0",
						"status":               "ACTIVE",
						"type":                 "SYSTEM_DEFINED",
						"models": []map[string]any{
							{"modelArn": "arn:aws:bedrock:us-east-1::foundation-model/anthropic.claude-sonnet-4-5-20250929-v1:0"},
							{"modelArn": "arn:aws:bedrock:us-west-2::foundation-model/anthropic.claude-sonnet-4-5-20250929-v1:0"},
						},
					},
					{
						"inferenceProfileId":   "us.anthropic.claude-legacy",
						"inferenceProfileName": "Retired profile",
						"inferenceProfileArn":  "arn:aws:bedrock:us-east-1:111:inference-profile/us.anthropic.claude-legacy",
						"status":               "FAILED",
						"type":                 "SYSTEM_DEFINED",
						"models":               []map[string]any{},
					},
				},
			})
		case "APPLICATION":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"inferenceProfileSummaries": []map[string]any{
					{
						"inferenceProfileId":   "arn:aws:bedrock:us-east-1:111:application-inference-profile/custom-1",
						"inferenceProfileName": "Custom cost tracker",
						"inferenceProfileArn":  "arn:aws:bedrock:us-east-1:111:application-inference-profile/custom-1",
						"status":               "ACTIVE",
						"type":                 "APPLICATION",
						"models": []map[string]any{
							{"modelArn": "arn:aws:bedrock:us-east-1::foundation-model/amazon.nova-pro-v1:0"},
						},
					},
				},
			})
		default:
			http.Error(w, "unexpected type filter", http.StatusBadRequest)
		}
	})

	models, err := discoverBedrockModelsWithClient(t.Context(), client)
	testutil.FailErr(t, "discover bedrock models", err)
	if len(models) != 2 {
		t.Fatalf("models = %+v, want system-defined + application merged (failed status dropped)", models)
	}
	if models[0].ID != "arn:aws:bedrock:us-east-1:111:application-inference-profile/custom-1" {
		t.Fatalf("first = %+v", models[0])
	}
	if models[0].PricedAs != "amazon.nova-pro-v1:0" {
		t.Fatalf("application profile priced_as = %q, want foundation id from routed model ARN", models[0].PricedAs)
	}
	if models[1].ID != "us.anthropic.claude-sonnet-4-5-20250929-v1:0" {
		t.Fatalf("second = %+v", models[1])
	}
	if models[1].PricedAs != "anthropic.claude-sonnet-4-5-20250929-v1:0" {
		t.Fatalf("system profile priced_as = %q, want foundation id from routed model ARN", models[1].PricedAs)
	}
}

func TestDiscoverBedrockModelsApplicationFailureKeepsSystemDefined(t *testing.T) {
	client := testBedrockClient(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Query().Get("type") {
		case "SYSTEM_DEFINED":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"inferenceProfileSummaries": []map[string]any{
					{
						"inferenceProfileId":   "us.anthropic.claude-haiku-4-5-20251001-v1:0",
						"inferenceProfileName": "US Claude Haiku",
						"inferenceProfileArn":  "arn:aws:bedrock:us-east-1:111:inference-profile/us.anthropic.claude-haiku-4-5-20251001-v1:0",
						"status":               "ACTIVE",
						"type":                 "SYSTEM_DEFINED",
						"models":               []map[string]any{},
					},
				},
			})
		case "APPLICATION":
			http.Error(w, `{"message":"access denied"}`, http.StatusForbidden)
		default:
			http.Error(w, "unexpected type filter", http.StatusBadRequest)
		}
	})

	models, err := discoverBedrockModelsWithClient(t.Context(), client)
	testutil.FailErr(t, "discover bedrock models", err)
	if len(models) != 1 || models[0].ID != "us.anthropic.claude-haiku-4-5-20251001-v1:0" {
		t.Fatalf("models = %+v, want system-defined listing preserved despite application list failure", models)
	}
}

// A stored Bedrock API key authenticates discovery even with ambient AWS
// credentials present: both clients list SigV4 ahead of bearer, so the explicit
// scheme preference keeps the key from being ignored.
func TestBedrockControlPlaneAuthOptsPreferStoredKeyOverAmbientCredentials(t *testing.T) {
	var gotAuth string
	// TLS: the SDK refuses to put a bearer token on a plaintext request.
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		_ = json.NewEncoder(w).Encode(map[string]any{"inferenceProfileSummaries": []map[string]any{}})
	}))
	t.Cleanup(srv.Close)

	cfg := aws.Config{
		Region:      "us-east-1",
		Credentials: credentials.NewStaticCredentialsProvider("AKIAAMBIENT", "ambient-secret", ""),
	}
	opts := append(
		providerauth.BedrockControlOptions("bedrock-api-key"),
		func(o *bedrock.Options) {
			o.BaseEndpoint = aws.String(srv.URL)
			o.HTTPClient = srv.Client()
		},
	)
	client := bedrock.NewFromConfig(cfg, opts...)

	_, err := discoverBedrockModelsWithClient(t.Context(), client)
	testutil.FailErr(t, "discover with stored key", err)
	if gotAuth != "Bearer bedrock-api-key" {
		t.Fatalf("Authorization = %q, want the stored Bedrock API key as a bearer token", gotAuth)
	}
}

// With no stored key the ambient chain still signs, so ambient mode keeps
// working for IAM-role and ~/.aws users.
func TestBedrockControlPlaneAuthOptsEmptyKeyFallsBackToSigV4(t *testing.T) {
	var gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		_ = json.NewEncoder(w).Encode(map[string]any{"inferenceProfileSummaries": []map[string]any{}})
	}))
	t.Cleanup(srv.Close)

	if opts := providerauth.BedrockControlOptions("  "); opts != nil {
		t.Fatalf("blank key produced %d client options, want none", len(opts))
	}
	cfg := aws.Config{
		Region:      "us-east-1",
		Credentials: credentials.NewStaticCredentialsProvider("AKIAAMBIENT", "ambient-secret", ""),
	}
	client := bedrock.NewFromConfig(cfg, func(o *bedrock.Options) { o.BaseEndpoint = aws.String(srv.URL) })

	_, err := discoverBedrockModelsWithClient(t.Context(), client)
	testutil.FailErr(t, "discover with ambient credentials", err)
	if !strings.HasPrefix(gotAuth, "AWS4-HMAC-SHA256") {
		t.Fatalf("Authorization = %q, want an ambient SigV4 signature", gotAuth)
	}
}

// bedrockAvailabilityServer serves ListInferenceProfiles for one profile plus
// the two availability reads that grade it.
func bedrockAvailabilityServer(
	t *testing.T,
	profileID, foundationModel string,
	catalog []string,
	agreement string,
) *bedrock.Client {
	t.Helper()
	return testBedrockClient(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/inference-profiles":
			if r.URL.Query().Get("type") != "SYSTEM_DEFINED" {
				_ = json.NewEncoder(w).Encode(map[string]any{"inferenceProfileSummaries": []map[string]any{}})
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"inferenceProfileSummaries": []map[string]any{{
					"inferenceProfileId":  profileID,
					"inferenceProfileArn": "arn:aws:bedrock:us-east-1:111:inference-profile/" + profileID,
					"status":              "ACTIVE",
					"type":                "SYSTEM_DEFINED",
					"models": []map[string]any{
						{"modelArn": "arn:aws:bedrock:us-east-1::foundation-model/" + foundationModel},
					},
				}},
			})
		case r.URL.Path == "/foundation-models":
			summaries := make([]map[string]any, 0, len(catalog))
			for _, id := range catalog {
				summaries = append(summaries, map[string]any{
					"modelId":        id,
					"modelArn":       "arn:aws:bedrock:us-east-1::foundation-model/" + id,
					"modelLifecycle": map[string]any{"status": "ACTIVE"},
				})
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"modelSummaries": summaries})
		case strings.HasPrefix(r.URL.Path, "/foundation-model-availability/"):
			_ = json.NewEncoder(w).Encode(map[string]any{
				"modelId":                 foundationModel,
				"authorizationStatus":     "AUTHORIZED",
				"entitlementAvailability": "AVAILABLE",
				"regionAvailability":      "AVAILABLE",
				"agreementAvailability":   map[string]any{"status": agreement},
			})
		default:
			http.NotFound(w, r)
		}
	})
}

// A routed foundation model the account has not accepted an agreement for is
// graded uncallable, so it never reaches the assignable list.
func TestDiscoverBedrockModelsMarksUnacceptedAgreementUncallable(t *testing.T) {
	client := bedrockAvailabilityServer(t,
		"us.openai.gpt-5.6-luna", "openai.gpt-5.6-luna",
		[]string{"openai.gpt-5.6-luna"}, "NOT_AVAILABLE")

	models, err := discoverBedrockModelsWithClient(t.Context(), client)
	testutil.FailErr(t, "discover bedrock models", err)
	if len(models) != 1 {
		t.Fatalf("models = %+v, want the profile retained with a callability grade", models)
	}
	if !modelinfo.Refused(models[0].Callable) {
		t.Fatalf("callable = %+v, want unsupported for an unaccepted agreement", models[0].Callable)
	}
	if len(models[0].Callable.Sources) == 0 || models[0].Callable.Sources[0] != bedrockAgreementSource {
		t.Fatalf("callable sources = %v, want %q", models[0].Callable.Sources, bedrockAgreementSource)
	}
}

// A routed model absent from ListFoundationModels has been retired.
func TestDiscoverBedrockModelsMarksRetiredFoundationModelUncallable(t *testing.T) {
	client := bedrockAvailabilityServer(t,
		"us.meta.llama3-2-1b-instruct-v1:0", "meta.llama3-2-1b-instruct-v1:0",
		[]string{"amazon.nova-pro-v1:0"}, "AVAILABLE")

	models, err := discoverBedrockModelsWithClient(t.Context(), client)
	testutil.FailErr(t, "discover bedrock models", err)
	if len(models) != 1 || !modelinfo.Refused(models[0].Callable) {
		t.Fatalf("models = %+v, want the retired profile graded uncallable", models)
	}
	if models[0].Callable.Sources[0] != bedrockRetiredSource {
		t.Fatalf("callable sources = %v, want %q", models[0].Callable.Sources, bedrockRetiredSource)
	}
}

// An accepted agreement on a listed model grades nothing: AVAILABLE is not
// evidence the model will serve, so the entry stays unknown and assignable.
func TestDiscoverBedrockModelsLeavesAvailableAgreementUngraded(t *testing.T) {
	client := bedrockAvailabilityServer(t,
		"us.amazon.nova-pro-v1:0", "amazon.nova-pro-v1:0",
		[]string{"amazon.nova-pro-v1:0"}, "AVAILABLE")

	models, err := discoverBedrockModelsWithClient(t.Context(), client)
	testutil.FailErr(t, "discover bedrock models", err)
	if len(models) != 1 {
		t.Fatalf("models = %+v, want one profile", models)
	}
	if models[0].Callable.State != "" && models[0].Callable.State != modelinfo.CapabilityUnknown {
		t.Fatalf("callable = %+v, want ungraded for an available agreement", models[0].Callable)
	}
}

// An identity whose policy omits the availability reads must keep its full
// listing rather than losing every model to a failed probe.
func TestDiscoverBedrockModelsAvailabilityDeniedFailsOpen(t *testing.T) {
	client := testBedrockClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/inference-profiles" {
			if r.URL.Query().Get("type") != "SYSTEM_DEFINED" {
				_ = json.NewEncoder(w).Encode(map[string]any{"inferenceProfileSummaries": []map[string]any{}})
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"inferenceProfileSummaries": []map[string]any{{
					"inferenceProfileId":  "us.amazon.nova-pro-v1:0",
					"inferenceProfileArn": "arn:aws:bedrock:us-east-1:111:inference-profile/us.amazon.nova-pro-v1:0",
					"status":              "ACTIVE",
					"type":                "SYSTEM_DEFINED",
					"models": []map[string]any{
						{"modelArn": "arn:aws:bedrock:us-east-1::foundation-model/amazon.nova-pro-v1:0"},
					},
				}},
			})
			return
		}
		http.Error(w, `{"message":"access denied"}`, http.StatusForbidden)
	})

	models, err := discoverBedrockModelsWithClient(t.Context(), client)
	testutil.FailErr(t, "discover bedrock models", err)
	if len(models) != 1 {
		t.Fatalf("models = %+v, want the listing preserved when availability reads are denied", models)
	}
	if modelinfo.Refused(models[0].Callable) {
		t.Fatal("a denied availability probe must not grade a model uncallable")
	}
}

// An empty ListFoundationModels answer is unusable, not proof that every routed
// model retired.
func TestDiscoverBedrockModelsEmptyCatalogGradesNothingRetired(t *testing.T) {
	client := bedrockAvailabilityServer(t,
		"us.amazon.nova-pro-v1:0", "amazon.nova-pro-v1:0",
		nil, "AVAILABLE")

	models, err := discoverBedrockModelsWithClient(t.Context(), client)
	testutil.FailErr(t, "discover bedrock models", err)
	if len(models) != 1 || modelinfo.Refused(models[0].Callable) {
		t.Fatalf("models = %+v, want no retirement grade from an empty catalog", models)
	}
}

func TestDiscoverBedrockModelsSystemFailurePropagates(t *testing.T) {
	client := testBedrockClient(t, func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"message":"unauthorized"}`, http.StatusForbidden)
	})
	_, err := discoverBedrockModelsWithClient(t.Context(), client)
	if err == nil {
		t.Fatal("expected error when the primary system-defined listing fails")
	}
	if !strings.Contains(err.Error(), "unauthorized") && !strings.Contains(err.Error(), "403") {
		t.Fatalf("error = %v, want context about the ListInferenceProfiles failure", err)
	}
}
