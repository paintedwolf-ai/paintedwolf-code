package sourcecontracts

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"testing"

	contractfixture "github.com/lycaon/lycaon/internal/api/contractfixture"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestSourceRawImagesCarryAnInertDocumentPolicy(t *testing.T) {
	srv := contractfixture.NewTestServer(t)
	root := t.TempDir()
	p := contractfixture.CreateProjectForTest(t, srv, root)
	const svg = `<svg xmlns="http://www.w3.org/2000/svg"><script>alert(1)</script><rect width="10" height="10"/></svg>`
	testutil.FailErr(t, "write SVG", os.WriteFile(filepath.Join(root, "drawing.svg"), []byte(svg), 0600))
	query := url.Values{"root_id": {p.Roots[0].ID}, "path": {"drawing.svg"}}
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, contractfixture.NewAuthedRequest(http.MethodGet, "/v1/projects/"+p.ID+"/source/raw?"+query.Encode(), nil))
	if rec.Code != http.StatusOK || rec.Body.String() != svg {
		t.Fatalf("raw SVG = %d %s", rec.Code, rec.Body.String())
	}
	for header, want := range map[string]string{
		"Content-Type":            "image/svg+xml",
		"X-Content-Type-Options":  "nosniff",
		"Content-Security-Policy": "sandbox; default-src 'none'; style-src 'unsafe-inline'; img-src data:",
	} {
		if got := rec.Header().Get(header); got != want {
			t.Fatalf("%s = %q, want %q", header, got, want)
		}
	}
}
