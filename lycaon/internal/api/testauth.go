package api

import "net/http"

// TestAuthHeader is the Authorization value for httptest and integration helpers.
func TestAuthHeader() string {
	return "Bearer " + TestAPIToken
}

// WithTestAuth sets Authorization on the request for /v1 routes.
func WithTestAuth(req *http.Request) {
	req.Header.Set("Authorization", TestAuthHeader())
}
