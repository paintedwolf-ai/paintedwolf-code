package httpaction

import (
	"encoding/base64"
	"errors"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/secretmatch"
	"github.com/lycaon/lycaon/internal/toolrejection"
)

func TestJarOutputProtectsReplacedValuesAndDiagnosticFacts(t *testing.T) {
	var output JarOutput
	const older = "fixture-cookie-seed"
	const newer = older + "-rotated"
	output.protect(older)
	if output.Text(older) != secretmatch.RedactedMarker {
		t.Fatal("initial cookie was visible")
	}
	output.protect(newer)
	encoded := base64.StdEncoding.EncodeToString([]byte(newer))
	reason := older + " " + newer + " " + encoded
	if got := output.Text(reason); got != strings.TrimSpace(strings.Repeat(secretmatch.RedactedMarker+" ", 3)) {
		t.Fatal("overlap or serialized spelling was visible")
	}
	cause := &toolrejection.ToolReject{Code: "HTTP_REQUEST_FAILED", Data: map[string]any{
		"reason": reason, "redirects": []any{map[string]any{"url": newer}}, "names": []string{older}, "issuers": map[string]string{"issuer": encoded}, "requests_started": 2,
	}}
	err := output.diagnostic(cause)
	var reject *toolrejection.ToolReject
	if !errors.Is(err, cause) || !errors.As(err, &reject) || reject == cause || reject.Code != cause.Code || reject.Data["requests_started"] != 2 {
		t.Fatal("diagnostic lost its transport facts")
	}
	if strings.Contains(err.Error(), older) || reject.Data["issuers"].(map[string]string)["issuer"] != secretmatch.RedactedMarker || reject.Data["names"].([]string)[0] != secretmatch.RedactedMarker || reject.Data["redirects"].([]any)[0].(map[string]any)["url"] != secretmatch.RedactedMarker {
		t.Fatal("managed value appeared in diagnostic")
	}
	if cause.Data["reason"] != reason {
		t.Fatal("projection mutated the original cause")
	}
}
