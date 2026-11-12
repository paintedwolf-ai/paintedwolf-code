package curl

import (
	"maps"
	"slices"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/lycaon/lycaon/internal/commandprogram"
	"github.com/lycaon/lycaon/internal/secretcap"
	"github.com/lycaon/lycaon/internal/secretmatch"
	"github.com/lycaon/lycaon/internal/testutil"
)

// A reference in curl argv lies in a declared slot exactly when curl sends it
// in a request placement the http_request owner admits.
func TestCurlReferencesReachOnlyRequestPlacements(t *testing.T) {
	h := newHarness(t)
	h.write("body.txt", "file body\n")
	ref := secretmatch.ReferenceToken(uuid.NewString())
	const target = "http://127.0.0.1:9/api"
	cases := []struct {
		name  string
		words []string
		sink  bool
	}{
		{"header value", []string{"-H", "Authorization: Bearer " + ref}, true},
		{"host header", []string{"-H", "Host: " + ref}, true},
		{"basic credentials", []string{"-u", "user:" + ref}, true},
		{"inline data", []string{"-d", "token=" + ref, "-d", "next=1"}, true},
		{"raw data", []string{"--data-raw", "@" + ref}, true},
		{"inline json", []string{"--json", `{"token":"` + ref + `"}`}, true},
		{"inline form value", []string{"-F", "token=" + ref, "-F", "file=@body.txt"}, true},
		{"key passphrase", []string{"--pass", ref}, true},
		{"certificate password", []string{"-E", "client.pem:" + ref}, false},
		{"url", []string{"-H", "Accept: */*"}, false},
		{"output path", []string{"-o", ref}, false},
		{"user agent", []string{"-A", ref}, false},
		{"cookie", []string{"-b", "session=" + ref}, false},
		{"query data", []string{"-G", "-d", "token=" + ref}, false},
		{"urlencoded data", []string{"--data-urlencode", "token=" + ref}, false},
		{"data beside a file", []string{"-d", "token=" + ref, "-d", "@body.txt"}, false},
		{"form file path", []string{"-F", "file=@" + ref}, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			url := target
			if c.name == "url" {
				url += "?token=" + ref
			}
			prepared, _, err := h.prepare(append(c.words, url)...)
			testutil.FailErr(t, "prepare", err)
			args := prepared.Review.Arguments()
			// A partial token refuses as well as a complete one.
			use := secretcap.ReferenceUseOutsideSlots(args, h.owners.SecretSlots())
			outside := use.Complete || use.Malformed
			inside := secretcap.ReferenceUseInSlots(args, h.owners.SecretSlots()).Complete
			if c.sink && (outside || !inside) {
				t.Fatalf("a sink reference: outside %v, inside %v, arguments %v", outside, inside, args)
			}
			if !c.sink && !outside {
				t.Fatalf("a reference outside every request placement reached a slot: %v", args)
			}
		})
	}
}

// The request curl sends carries the admitted record's resolved values at the
// record's own paths; nothing else in the request changes.
func TestCurlSendsTheResolvedRecord(t *testing.T) {
	h := newHarness(t)
	ref := secretmatch.ReferenceToken(uuid.NewString())
	prepared, _, err := h.prepare("-H", "Authorization: Bearer "+ref, "-H", "X-Plain: kept", "-u", "user:"+ref, "-d", "token="+ref, "-A", "agent", "http://127.0.0.1:9/api")
	testutil.FailErr(t, "prepare", err)
	resolved := replaceReference(prepared.Review.Arguments(), ref, "resolved-value")
	tc := h.tc
	tc.Effects.Secrets = secretcap.NewResolutionForTest(resolved.(map[string]any), nil)
	inv := newInvocation(t.Context(), prepared, tc, commandprogram.Streams{})
	args, err := inv.requestArgs("")
	testutil.FailErr(t, "request args", err)
	headers := args["headers"].([]any)
	if got := headers[0].(map[string]any)["value"]; got != "Bearer resolved-value" {
		t.Fatalf("authorization header = %v", got)
	}
	if got := headers[1].(map[string]any)["value"]; got != "kept" {
		t.Fatalf("plain header = %v", got)
	}
	if got := args["auth"].(map[string]any)["password"]; got != "resolved-value" {
		t.Fatalf("password = %v", got)
	}
	if got := args["body_text"]; got != "token=resolved-value" {
		t.Fatalf("body = %v", got)
	}
	if !slices.ContainsFunc(headers, func(h any) bool { return h.(map[string]any)["value"] == "agent" }) {
		t.Fatalf("user agent dropped: %v", headers)
	}
}

func replaceReference(value any, ref, with string) any {
	switch typed := value.(type) {
	case string:
		return strings.ReplaceAll(typed, ref, with)
	case []any:
		out := make([]any, len(typed))
		for i, item := range typed {
			out[i] = replaceReference(item, ref, with)
		}
		return out
	case map[string]any:
		out := maps.Clone(typed)
		for k, v := range typed {
			out[k] = replaceReference(v, ref, with)
		}
		return out
	}
	return value
}

// Every file curl reads or writes is a reviewed path target.
func TestCurlPathTargetsNameEveryFileItTouches(t *testing.T) {
	h := newHarness(t)
	prepared, _, err := h.prepare("--cacert", "ca.pem", "--capath", "cas", "-E", "client.pem", "--key", "client.key",
		"-T", "upload.bin", "-b", "cookies.txt", "-o", "out.txt", "-D", "headers.txt", "-c", "jar.txt", "--unix-socket", "daemon.sock", "http://127.0.0.1:9/api")
	testutil.FailErr(t, "prepare", err)
	got := map[string]bool{}
	for _, target := range prepared.Review.PathTargets {
		got[target.Path] = target.Write
	}
	reads := []string{"ca.pem", "cas", "client.pem", "client.key", "upload.bin", "cookies.txt", "daemon.sock"}
	for _, path := range reads {
		if write, ok := got[path]; !ok || write {
			t.Errorf("read target %s: present %v, write %v", path, ok, write)
		}
	}
	for _, path := range []string{"out.txt", "headers.txt", "jar.txt"} {
		if write, ok := got[path]; !ok || !write {
			t.Errorf("write target %s: present %v, write %v", path, ok, write)
		}
	}
}

// The request body forms that read files name them as targets too.
func TestCurlBodyFormsNameTheirFiles(t *testing.T) {
	h := newHarness(t)
	for _, words := range [][]string{
		{"--json", "@body.json"},
		{"--data-urlencode", "name@encoded.txt"},
		{"--data-urlencode", "@encoded.txt"},
		{"-F", "a=@part.bin;type=text/plain", "-F", "b=<field.txt"},
	} {
		prepared, _, err := h.prepare(append(words, "http://127.0.0.1:9/api")...)
		testutil.FailErr(t, "prepare", err)
		var paths []string
		for _, target := range prepared.Review.PathTargets {
			if target.Write {
				t.Fatalf("%v names a write target %s", words, target.Path)
			}
			paths = append(paths, target.Path)
		}
		want := map[string][]string{"--json": {"body.json"}, "--data-urlencode": {"encoded.txt"}, "-F": {"part.bin", "field.txt"}}[words[0]]
		if !slices.Equal(paths, want) {
			t.Fatalf("%v targets %v, want %v", words, paths, want)
		}
	}
}
