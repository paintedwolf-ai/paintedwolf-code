package modeladmin

import (
	"testing"

	"github.com/lycaon/lycaon/internal/llm"
)

func TestValidateProviderBaseURLRejectsEmbeddedAuthorityAndSuffixes(t *testing.T) {
	for _, raw := range []string{
		"https://user:secret@example.com/v1",
		"https://example.com/v1?api-version=old",
		"https://example.com/v1#fragment",
	} {
		if err := validateProviderBaseURL(llm.EndpointStyleURL, raw); err == nil {
			t.Errorf("validateProviderBaseURL(%q) succeeded", raw)
		}
	}
	if err := validateProviderBaseURL(llm.EndpointStyleURL, "https://example.com/v1"); err != nil {
		t.Fatalf("valid provider URL rejected: %v", err)
	}
}

func TestValidateProviderBaseURL(t *testing.T) {
	cases := []struct {
		name    string
		style   llm.EndpointStyle
		in      string
		wantErr bool
	}{
		{"https", llm.EndpointStyleURL, "https://api.example.com/v1", false},
		{"http", llm.EndpointStyleURL, "http://localhost:8080", false},
		{"with path", llm.EndpointStyleURL, "https://api.example.com/inference/v1", false},
		{"empty URL", llm.EndpointStyleURL, "", true},
		{"no scheme", llm.EndpointStyleURL, "api.example.com", true},
		{"junk", llm.EndpointStyleURL, "::not_a_url::", true},
		{"ftp scheme", llm.EndpointStyleURL, "ftp://example.com", true},
		{"file scheme", llm.EndpointStyleURL, "file:///etc/passwd", true},
		{"http no host", llm.EndpointStyleURL, "http://", true},
		{"region", llm.EndpointStyleRegion, "us-east-1", false},
		{"empty region", llm.EndpointStyleRegion, "", true},
		{"region URL", llm.EndpointStyleRegion, "https://bedrock.us-east-1.amazonaws.com", true},
		{"region whitespace", llm.EndpointStyleRegion, "us east 1", true},
		{"region uppercase", llm.EndpointStyleRegion, "US-EAST-1", true},
		{"empty derived", llm.EndpointStyleDerived, "", false},
		{"derived rejects client value", llm.EndpointStyleDerived, "https://api.example.com", true},
	}
	for _, tc := range cases {
		err := validateProviderBaseURL(tc.style, tc.in)
		if (err != nil) != tc.wantErr {
			t.Errorf("validateProviderBaseURL(%q, %q) err=%v wantErr=%v", tc.style, tc.in, err, tc.wantErr)
		}
	}
}
