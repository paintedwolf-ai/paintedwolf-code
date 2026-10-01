package preflight

import (
	"strings"
	"time"

	"github.com/lycaon/lycaon/internal/configdir"
	"github.com/lycaon/lycaon/internal/observability"
	wire "github.com/lycaon/lycaon/pkg/api"
)

// HostFacts are the environment facts the stop-screen report quotes. They are
// injected, like Env, so the report never reads ambient host state.
type HostFacts struct {
	AppVersion string
	// Build is the VCS revision when the binary was stamped. Empty is normal for
	// a local build and is omitted rather than reported as blank.
	Build         string
	SchemaVersion int
	OSName        string
	OSVersion     string
	OSFloor       string
	Arch          string
}

// BuildCatastrophicDetail assembles the copyable stop-screen report for a result
// whose declared tier is catastrophic.
//
// It is the only constructor of wire.CatastrophicDetail. Every emitted string
// passes through the secret scrubber and RedactPath, and fields are copied from
// an explicit allowlist, never from a spread or a marshalled error.
func BuildCatastrophicDetail(res Result, facts HostFacts, now time.Time) *wire.CatastrophicDetail {
	if res.Code == "" {
		return nil
	}
	return &wire.CatastrophicDetail{
		Code:       res.Code,
		Resolution: strings.TrimSpace(res.Detail["resolution"]),
		ProbeID:    res.ID,

		AppVersion:    safeFact(facts.AppVersion),
		Build:         safeFact(facts.Build),
		SchemaVersion: facts.SchemaVersion,

		OSName:         safeFact(facts.OSName),
		OSVersion:      safeFact(facts.OSVersion),
		OSFloor:        safeFact(facts.OSFloor),
		Arch:           safeFact(facts.Arch),
		ConfigDirLabel: configdir.Label(), // never the real path: this report gets pasted publicly

		Facts:      safeFacts(res.Detail),
		ObservedAt: now.UTC().Format(time.RFC3339),
	}
}

// safeFacts copies a probe's detail map through safeFact, dropping secret-named
// keys, whether or not the probe redacted at the source.
func safeFacts(detail map[string]string) map[string]string {
	if len(detail) == 0 {
		return nil
	}
	out := make(map[string]string, len(detail))
	for k, v := range detail {
		// "resolution" is promoted to its own field; repeating it as a fact would
		// make the report read as if the probe emitted it twice.
		if k == "resolution" {
			continue
		}
		if observability.IsSecretFieldName(k) {
			continue
		}
		out[k] = safeFact(v)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// pathOutsideHome stands in for an absolute path the report will not carry.
const pathOutsideHome = "(path outside home)"

// safeFact redacts one emitted string: secrets scrubbed, then any absolute path
// rewritten to a home-relative or symbolic form.
//
// RedactPath rewrites only $HOME and the filesystem root; any other absolute path
// becomes pathOutsideHome, since it can expose a directory layout, mount, or employer.
func safeFact(v string) string {
	v = strings.TrimSpace(v)
	if v == "" {
		return ""
	}
	v = observability.RedactString(v)
	if !strings.HasPrefix(v, "/") {
		return v
	}
	if redacted := RedactPath(v); !strings.HasPrefix(redacted, "/") {
		return redacted
	}
	return pathOutsideHome
}
