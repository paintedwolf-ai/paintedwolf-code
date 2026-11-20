package httpaction

import (
	"testing"

	"github.com/lycaon/lycaon/internal/toolrejection"
)

func TestNamedJarsRequireStorageAndReleaseMetadata(t *testing.T) {
	service, _ := testSecrets(t)
	for name, deps := range map[string]Deps{
		"absent":           {},
		"missing storage":  {Secrets: service},
		"missing metadata": {Jars: service.Jars},
	} {
		t.Run(name, func(t *testing.T) {
			tc := sessionContext(t.TempDir(), "jar-dependencies")
			cookie, _, err := openCookieJar(t.Context(), deps, tc, map[string]any{"cookie_jar": "login"})
			reject := toolrejection.AsToolReject(err)
			if cookie != nil || reject == nil || reject.Code != CookieJarFailedCode {
				t.Fatalf("cookie jar with incomplete dependencies = %v, %v", cookie, err)
			}
			token, _, err := openTokenJar(t.Context(), deps, tc, map[string]any{"token_jar": "login"})
			reject = toolrejection.AsToolReject(err)
			if token != nil || reject == nil || reject.Code != TokenJarFailedCode {
				t.Fatalf("token jar with incomplete dependencies = %v, %v", token, err)
			}
		})
	}
}
