package security

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
	wire "github.com/lycaon/lycaon/pkg/api"
	"github.com/lycaon/lycaon/test/wiring"
)

type sourceEncodingJourney struct {
	encoding      string
	path          string
	originalText  string
	editedText    string
	originalBytes []byte
	editedBytes   []byte
}

func TestProjectSourceEncodingPipelineE2E(t *testing.T) {
	h := wiring.BuildForTest(t)
	httpSrv := httptest.NewServer(h.Server)
	t.Cleanup(httpSrv.Close)
	root := t.TempDir()

	encodings := []string{
		"utf-8", "utf-8-bom", "utf-16le", "utf-16le-bom", "utf-16be", "utf-16be-bom",
	}
	journeys := make([]sourceEncodingJourney, 0, len(encodings))
	for _, encoding := range encodings {
		original := "original " + encoding + "\r\n世界🐺\nA\u030a"
		edited := "edited " + encoding + "\n世界🐺\r\nA\u030a"
		before, err := testutil.ReferenceEncodeText(original, encoding)
		testutil.FailErr(t, "reference encode original", err)
		after, err := testutil.ReferenceEncodeText(edited, encoding)
		testutil.FailErr(t, "reference encode edit", err)
		journey := sourceEncodingJourney{
			encoding: encoding, path: encoding + ".txt",
			originalText: original, editedText: edited,
			originalBytes: before, editedBytes: after,
		}
		testutil.FailErr(t, "seed encoded source", os.WriteFile(filepath.Join(root, journey.path), before, 0o644))
		journeys = append(journeys, journey)
	}
	conflictBytes, err := testutil.ReferenceEncodeText("opened\r\n🐺", "utf-16be-bom")
	testutil.FailErr(t, "encode conflict fixture", err)
	testutil.FailErr(t, "seed conflict source", os.WriteFile(filepath.Join(root, "conflict.txt"), conflictBytes, 0o644))

	project := createAPIProjectAtPath(t, httpSrv.URL, root)
	params := map[string]string{"id": project.ID}
	for _, journey := range journeys {
		readPath := "/v1/projects/{id}/source?path=" + journey.path
		if journey.encoding == "utf-16le" || journey.encoding == "utf-16be" {
			automatic := openAPIGetJSON[wire.ProjectSourceReadResponse](t, httpSrv.URL, readPath, params, http.StatusOK)
			if !automatic.Binary || automatic.Content != "" || automatic.Encoding != "" {
				t.Fatalf("%s automatic read = %+v, want binary metadata", journey.encoding, automatic)
			}
			readPath += "&decode_as=" + journey.encoding
		}
		opened := openAPIGetJSON[wire.ProjectSourceReadResponse](t, httpSrv.URL, readPath, params, http.StatusOK)
		assertSourceJourneyRead(t, opened, journey.path, journey.originalText, journey.encoding, journey.originalBytes)
		written := openAPIPutJSON[wire.ProjectSourceWriteResponse](t, httpSrv.URL,
			"/v1/projects/{id}/source", params,
			sourcePipelinePutBody(t, journey.path, journey.editedText, journey.encoding, opened.SHA256), http.StatusOK)
		if written.SHA256 != sourcePipelineSHA(journey.editedBytes) || written.SizeBytes != int64(len(journey.editedBytes)) {
			t.Fatalf("%s write result = %+v", journey.encoding, written)
		}
		assertSourcePipelineDisk(t, root, journey.path, journey.editedBytes)
		reopened := openAPIGetJSON[wire.ProjectSourceReadResponse](t, httpSrv.URL, readPath, params, http.StatusOK)
		assertSourceJourneyRead(t, reopened, journey.path, journey.editedText, journey.encoding, journey.editedBytes)
	}

	walk := openAPIGetJSON[wire.SourceWalkResponse](t, httpSrv.URL,
		"/v1/projects/{id}/source/walk?baseline=presentation&limit=100", params, http.StatusOK)
	for _, journey := range journeys {
		effect := sourcePipelineEffect(t, walk, journey.path)
		comparison := openAPIGetJSON[wire.SourceComparison](t, httpSrv.URL,
			"/v1/projects/{id}/source/comparison?effect_id="+effect.ID,
			map[string]string{"id": project.ID}, http.StatusOK)
		if journey.encoding == "utf-16le" || journey.encoding == "utf-16be" {
			if comparison.Before.Availability != "binary" || comparison.After.Availability != "binary" ||
				comparison.Before.Sha256 != sourcePipelineSHA(journey.originalBytes) ||
				comparison.After.Sha256 != sourcePipelineSHA(journey.editedBytes) {
				t.Fatalf("%s history comparison = %+v", journey.encoding, comparison)
			}
			continue
		}
		if comparison.Before.Availability != "available" || comparison.After.Availability != "available" ||
			comparison.Before.Content != journey.originalText || comparison.After.Content != journey.editedText ||
			comparison.Before.Sha256 != sourcePipelineSHA(journey.originalBytes) ||
			comparison.After.Sha256 != sourcePipelineSHA(journey.editedBytes) {
			t.Fatalf("%s history comparison = %+v", journey.encoding, comparison)
		}
		openAPIPutJSON[wire.ProjectSourceWriteResponse](t, httpSrv.URL,
			"/v1/projects/{id}/source", params,
			sourcePipelinePutBody(t, journey.path, journey.originalText, journey.encoding, sourcePipelineSHA(journey.editedBytes)), http.StatusOK)
		assertSourcePipelineDisk(t, root, journey.path, journey.originalBytes)
	}

	first := journeys[0]
	openAPIDo(t, httpSrv.URL, http.MethodPut, "/v1/projects/{id}/source", params,
		sourcePipelinePutBody(t, first.path, "wrong encoding", "utf-16be-bom", sourcePipelineSHA(first.originalBytes)),
		http.StatusBadRequest)
	openAPIDo(t, httpSrv.URL, http.MethodPut, "/v1/projects/{id}/source", params,
		sourcePipelinePutBody(t, first.path, "binary\x00text", first.encoding, sourcePipelineSHA(first.originalBytes)),
		http.StatusUnsupportedMediaType)
	assertSourcePipelineDisk(t, root, first.path, first.originalBytes)

	conflictOpened := openAPIGetJSON[wire.ProjectSourceReadResponse](t, httpSrv.URL,
		"/v1/projects/{id}/source?path=conflict.txt", params, http.StatusOK)
	newerBytes, err := testutil.ReferenceEncodeText("newer\n世界", "utf-16be-bom")
	testutil.FailErr(t, "encode concurrent source", err)
	testutil.FailErr(t, "write concurrent source", os.WriteFile(filepath.Join(root, "conflict.txt"), newerBytes, 0o644))
	openAPIDo(t, httpSrv.URL, http.MethodPut, "/v1/projects/{id}/source", params,
		sourcePipelinePutBody(t, "conflict.txt", "stale", "utf-16be-bom", conflictOpened.SHA256),
		http.StatusConflict)
	assertSourcePipelineDisk(t, root, "conflict.txt", newerBytes)

	finalWalk := openAPIGetJSON[wire.SourceWalkResponse](t, httpSrv.URL,
		"/v1/projects/{id}/source/walk?baseline=presentation&limit=100", params, http.StatusOK)
	userRows := 0
	for _, file := range finalWalk.Files {
		for _, effect := range file.Effects {
			if effect.Origin != wire.SourceChangeOriginUser {
				continue
			}
			if file.Path == "conflict.txt" {
				t.Fatal("refused conflict was recorded as a user source change")
			}
			userRows++
		}
	}
	wantUserRows := len(journeys)*2 - 2
	if userRows != wantUserRows {
		t.Fatalf("user source ledger rows = %d, want %d successful writes only", userRows, wantUserRows)
	}
}

func sourcePipelinePutBody(t *testing.T, path, content, encoding, baseSHA string) string {
	t.Helper()
	body, err := json.Marshal(wire.PutProjectSourceRequest{
		Path: path, Content: content, Encoding: wire.SourceEncoding(encoding), BaseSHA256: baseSHA,
	})
	testutil.FailErr(t, "encode source write request", err)
	return string(body)
}

func sourcePipelineSHA(raw []byte) string {
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

func assertSourceJourneyRead(t *testing.T, got wire.ProjectSourceReadResponse, path, text, encoding string, raw []byte) {
	t.Helper()
	if got.Path != path || got.Content != text || string(got.Encoding) != encoding ||
		got.SHA256 != sourcePipelineSHA(raw) || got.SizeBytes != int64(len(raw)) {
		t.Fatalf("source read = %+v", got)
	}
}

func assertSourcePipelineDisk(t *testing.T, root, path string, want []byte) {
	t.Helper()
	got, err := os.ReadFile(filepath.Join(root, path))
	testutil.FailErr(t, "read source from disk", err)
	if !bytes.Equal(got, want) {
		t.Fatalf("%s disk bytes = %x, want %x", path, got, want)
	}
}

func sourcePipelineEffect(t *testing.T, walk wire.SourceWalkResponse, path string) wire.SourceWalkEffect {
	t.Helper()
	for _, file := range walk.Files {
		if file.Path == path && len(file.Effects) == 1 {
			return file.Effects[0]
		}
	}
	t.Fatalf("source walk missing one effect for %s: %+v", path, walk.Files)
	return wire.SourceWalkEffect{}
}
