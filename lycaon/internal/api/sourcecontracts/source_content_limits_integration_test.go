//go:build integration

package sourcecontracts

import (
	"bytes"
	"net/http"
	"os"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/lycaon/lycaon/internal/projectsource"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/textfile"
	contractfixture "github.com/lycaon/lycaon/internal/api/contractfixture"
	wire "github.com/lycaon/lycaon/pkg/api"
)

func TestSourceContentRoundTripAtSupportedLimits(t *testing.T) {
	cases := []struct{ name, text, encoding, eol string }{
		{"above ordinary JSON limit", strings.Repeat("a", 2<<20), textfile.UTF8, "lf"},
		{"exact raw limit", strings.Repeat("a", projectsource.SourceReadMaxBytes), textfile.UTF8, "lf"},
		{"JSON escaping", strings.Repeat("<", projectsource.SourceReadMaxBytes), textfile.UTF8, "lf"},
		{"UTF-16 expansion", strings.Repeat("界", (projectsource.SourceReadMaxBytes-2)/2), textfile.UTF16LEBOM, "lf"},
		{"CRLF expansion", strings.Repeat(strings.Repeat("x", 1022)+"\n", projectsource.SourceReadMaxBytes/1024), textfile.UTF8, "crlf"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			serialized := tc.text
			if tc.eol == "crlf" {
				serialized = strings.ReplaceAll(serialized, "\n", "\r\n")
			}
			raw, err := textfile.EncodeBounded(serialized, tc.encoding, textfile.LimitsForRaw(projectsource.SourceWriteMaxBytes))
			testutil.FailErr(t, "encode fixture", err)
			f := contractfixture.NewSourceContentFixture(t, raw)
			doc := f.Open(t)
			// A same-size edit exercises the exact raw cap, including BOM bytes.
			_, firstSize := utf8.DecodeRuneInString(tc.text)
			next := "b" + tc.text[firstSize:]
			w := contractfixture.SourceJSONRequest(t, f.Server, http.MethodPut, f.Url+"/editor-documents/"+doc.ID, wire.ReplaceEditorDocumentRequest{
				ClientID: "window", OperationID: uuid.NewString(), ExpectedRevision: doc.Revision, Content: next, EOL: tc.eol,
			})
			draft := contractfixture.DecodeSourceDocument(t, w)
			saved := contractfixture.DecodeSourceDocument(t, contractfixture.SourceJSONRequest(t, f.Server, http.MethodPost, f.Url+"/editor-documents/"+doc.ID+"/save", wire.SaveEditorDocumentRequest{
				ClientID: "window", ExpectedRevision: draft.Revision, OperationID: uuid.NewString(),
			}))
			if saved.Dirty {
				t.Fatal("saved document is dirty")
			}
			wantText := next
			if tc.eol == "crlf" {
				wantText = strings.ReplaceAll(wantText, "\n", "\r\n")
			}
			want, err := textfile.EncodeBounded(wantText, tc.encoding, textfile.LimitsForRaw(projectsource.SourceWriteMaxBytes))
			testutil.FailErr(t, "encode expected bytes", err)
			got, err := os.ReadFile(f.Path)
			testutil.FailErr(t, "read saved bytes", err)
			if !bytes.Equal(got, want) {
				t.Fatal("saved bytes differ from encoded draft")
			}
			reopened := f.Open(t)
			if f.Text(t, reopened) != next {
				t.Fatal("reopened document differs from saved draft")
			}
			// The direct full-replacement endpoint accepts the same supported content.
			w = contractfixture.SourceJSONRequest(t, f.Server, http.MethodPut, f.Url+"/source", wire.PutProjectSourceRequest{
				OperationID: uuid.NewString(), Path: "content.txt", RootID: f.Project.Roots[0].ID,
				Content: serialized, Encoding: wire.SourceEncoding(tc.encoding), BaseSHA256: textfile.SHA256(got),
			})
			if w.Code != http.StatusOK {
				t.Fatalf("source write status=%d body=%s", w.Code, w.Body.String())
			}
			got, err = os.ReadFile(f.Path)
			testutil.FailErr(t, "read replacement bytes", err)
			if !bytes.Equal(got, raw) {
				t.Fatal("direct replacement changed encoding or content")
			}
		})
	}
}

func TestEditorDraftRejectsUnsavableContentWithoutChangingRevision(t *testing.T) {
	f := contractfixture.NewSourceContentFixture(t, []byte("base\n"))
	doc := f.Open(t)
	for _, tc := range []struct {
		name, content, eol string
		status             int
		code               string
	}{
		{"raw overflow", strings.Repeat("a", projectsource.SourceWriteMaxBytes+1), "lf", http.StatusRequestEntityTooLarge, "source_content_too_large"},
		{"decoded overflow", strings.Repeat("a", 2*projectsource.SourceWriteMaxBytes+1), "lf", http.StatusRequestEntityTooLarge, "source_content_too_large"},
		{"EOL overflow", strings.Repeat("\n", projectsource.SourceWriteMaxBytes/2+1), "crlf", http.StatusRequestEntityTooLarge, "source_content_too_large"},
		{"binary", "a\x00b", "lf", http.StatusBadRequest, "invalid_request"},
		{"invalid line ending", "base\n", "unknown", http.StatusBadRequest, "invalid_request"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := contractfixture.SourceJSONRequest(t, f.Server, http.MethodPut, f.Url+"/editor-documents/"+doc.ID, wire.ReplaceEditorDocumentRequest{
				ClientID: "window", OperationID: uuid.NewString(), ExpectedRevision: doc.Revision, Content: tc.content, EOL: tc.eol,
			})
			contractfixture.AssertErrorResponse(t, w, tc.status, tc.code)
			after := f.Open(t)
			if after.Revision != doc.Revision || f.Text(t, after) != f.Text(t, doc) || after.Dirty {
				t.Fatal("rejected update changed the durable draft")
			}
		})
	}
}

func TestEditorOpenRejectsOversizeFile(t *testing.T) {
	f := contractfixture.NewSourceContentFixture(t, []byte(strings.Repeat("a", projectsource.SourceReadMaxBytes+1)))
	w := contractfixture.SourceJSONRequest(t, f.Server, http.MethodPost, f.Url+"/editor-documents", wire.OpenEditorDocumentRequest{
		Path: "content.txt", RootID: f.Project.Roots[0].ID, ClientID: "window",
	})
	contractfixture.AssertErrorResponse(t, w, http.StatusRequestEntityTooLarge, "source_content_too_large")
}
