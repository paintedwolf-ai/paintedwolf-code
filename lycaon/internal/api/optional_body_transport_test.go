package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/lycaon/lycaon/internal/api/httpio"
	"github.com/lycaon/lycaon/internal/blueprint"
	"github.com/lycaon/lycaon/internal/project"
	"github.com/lycaon/lycaon/internal/testutil"
	wire "github.com/lycaon/lycaon/pkg/api"
)

func TestOptionalCommandBodiesHonorTransportFraming(t *testing.T) {
	project.SetDefaultOpenPolicy(project.TestOpenPolicy())
	dir := t.TempDir()
	projects := project.NewMemoryRegistry()
	p, err := project.CreateWithRoot(t.Context(), projects, dir)
	testutil.FailErr(t, "create project", err)
	blueprints := blueprint.NewManager(blueprint.NewFileStoreForTest(dir))
	bp, err := blueprints.Create(t.Context(), p.ID, "Transport fixture", "", "plan", "# Transport fixture")
	testutil.FailErr(t, "create blueprint", err)
	server := NewServer(requiredTestDeps(t, Dependencies{Projects: projects, Blueprints: blueprints}), nil, TestAPIToken)
	for _, endpoint := range []struct{ name, path, invalidBody string }{
		{"abort delegation", "/v1/delegations/" + uuid.NewString() + "/abort", `{"reason":42}`},
		{"launch blueprint", fmt.Sprintf("/v1/projects/%s/blueprints/%s/launch", p.ID, bp.ID), `{"defer_start":"yes"}`},
	} {
		t.Run(endpoint.name, func(t *testing.T) {
			for _, tc := range []struct {
				name, body, mediaType string
				status                int
				code                  wire.ApiErrorCode
			}{
				{"typed JSON", endpoint.invalidBody, "application/json", http.StatusBadRequest, wire.ApiErrorCodeInvalidRequest},
				{"media type", `{}`, "text/plain", http.StatusUnsupportedMediaType, wire.ApiErrorCodeUnsupportedMediaType},
				{"body budget", strings.Repeat(" ", httpio.MaxJSONBody+1), "application/json", http.StatusRequestEntityTooLarge, wire.ApiErrorCodeBodyTooLarge},
			} {
				t.Run(tc.name, func(t *testing.T) {
					for _, chunked := range []bool{false, true} {
						t.Run(fmt.Sprintf("chunked=%t", chunked), func(t *testing.T) {
							req := newAuthedRequest(http.MethodPost, endpoint.path, strings.NewReader(tc.body))
							req.Header.Set("Content-Type", tc.mediaType)
							if chunked {
								req.ContentLength = -1
								req.TransferEncoding = []string{"chunked"}
							}
							out := httptest.NewRecorder()
							server.ServeHTTP(out, req)
							if out.Code != tc.status {
								t.Fatalf("status = %d, want %d: %s", out.Code, tc.status, out.Body.String())
							}
							var response wire.ErrorResponse
							testutil.FailErr(t, "decode error", json.Unmarshal(out.Body.Bytes(), &response))
							if response.Code != tc.code {
								t.Fatalf("code = %q, want %q", response.Code, tc.code)
							}
						})
					}
				})
			}
		})
	}
}
