package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/messageview"
	"github.com/lycaon/lycaon/internal/testutil"
	wire "github.com/lycaon/lycaon/pkg/api"
)

func TestChatContentPagesSearchAndRevision(t *testing.T) {
	srv := newTestServer(t)
	sess := createSessionAtPathOnServer(t, srv, t.TempDir(), wire.SessionPostureBuild)
	body := strings.Repeat("🌲 Needle\n", 5000)
	msg := wire.Message{ID: "retained", Role: wire.MessageRoleTool, ToolResult: &wire.ToolResult{ToolCallID: "call", Tool: "read", Content: body}}
	testutil.FailErr(t, "append retained result", srv.sessionStore.AppendMessages(t.Context(), sess.ID, msg))
	ref := messageview.ContentReference("tool_output", "call", body)
	query := url.Values{"field": {"tool_output"}, "tool_call_id": {"call"}, "sha256": {ref.SHA256}, "limit": {"1023"}}
	path := "/v1/sessions/" + sess.ID + "/messages/retained/content"
	read := func(endpoint string) *httptest.ResponseRecorder {
		t.Helper()
		rec := httptest.NewRecorder()
		srv.ServeHTTP(rec, newAuthedRequest(http.MethodGet, endpoint, nil))
		return rec
	}
	var rebuilt strings.Builder
	for offset := 0; ; {
		query.Set("offset", strconv.Itoa(offset))
		rec := read(path + "?" + query.Encode())
		if rec.Code != http.StatusOK {
			t.Fatalf("range status=%d body=%s", rec.Code, rec.Body.String())
		}
		var page wire.ChatContentPage
		testutil.FailErr(t, "decode range", json.Unmarshal(rec.Body.Bytes(), &page))
		rebuilt.WriteString(page.Text)
		if page.Complete {
			break
		}
		if page.EndOffset <= offset {
			t.Fatal("window did not advance")
		}
		offset = page.EndOffset
	}
	if rebuilt.String() != body {
		t.Fatal("paged Unicode content differs from recorded output")
	}
	query.Del("offset")
	query.Set("q", "needle")
	query.Set("limit", "500")
	matches := 0
	for pages := 0; ; pages++ {
		rec := read(path + "/search?" + query.Encode())
		if rec.Code != http.StatusOK {
			t.Fatalf("search status=%d: %s", rec.Code, rec.Body.String())
		}
		var found wire.ChatContentSearchPage
		testutil.FailErr(t, "decode matches", json.Unmarshal(rec.Body.Bytes(), &found))
		if pages == 0 && (len(found.Matches) != 500 || found.Matches[0].Offset != 2) {
			t.Fatalf("unexpected first matches: %d", len(found.Matches))
		}
		matches += len(found.Matches)
		if found.NextCursor == "" {
			break
		}
		query.Set("cursor", found.NextCursor)
	}
	if matches != 5000 {
		t.Fatalf("search pages found %d matches, want 5000", matches)
	}
	query.Set("q", "tree")
	if rec := read(path + "/search?" + query.Encode()); rec.Code != http.StatusBadRequest {
		t.Fatalf("cursor reused for another query status=%d", rec.Code)
	}
	query.Del("cursor")
	query.Del("q")
	query.Set("sha256", "stale")
	if rec := read(path + "?" + query.Encode()); rec.Code != http.StatusConflict {
		t.Fatalf("stale revision status=%d", rec.Code)
	}
	query.Set("sha256", ref.SHA256)
	query.Set("tool_call_id", "another-call")
	if rec := read(path + "?" + query.Encode()); rec.Code != http.StatusNotFound {
		t.Fatalf("wrong call status=%d", rec.Code)
	}
}

func TestChatContentRowsReachExactEndAndLocateUnicode(t *testing.T) {
	srv := newTestServer(t)
	sess := createSessionAtPathOnServer(t, srv, t.TempDir(), wire.SessionPostureBuild)
	body := strings.Repeat("🌲 complete words\n", 150) + "last line"
	msg := wire.Message{ID: "lines", Role: wire.MessageRoleTool, ToolResult: &wire.ToolResult{ToolCallID: "call", Tool: "read", Content: body}}
	testutil.FailErr(t, "append line result", srv.sessionStore.AppendMessages(t.Context(), sess.ID, msg))
	ref := messageview.ContentReference("tool_output", "call", body)
	query := url.Values{"field": {"tool_output"}, "tool_call_id": {"call"}, "sha256": {ref.SHA256}}
	read := func() wire.ChatContentPage {
		t.Helper()
		rec := httptest.NewRecorder()
		srv.ServeHTTP(rec, newAuthedRequest(http.MethodGet, "/v1/sessions/"+sess.ID+"/messages/lines/content?"+query.Encode(), nil))
		if rec.Code != http.StatusOK {
			t.Fatalf("row status=%d: %s", rec.Code, rec.Body.String())
		}
		var page wire.ChatContentPage
		testutil.FailErr(t, "decode rows", json.Unmarshal(rec.Body.Bytes(), &page))
		return page
	}
	var joined strings.Builder
	for row := 0; ; {
		query.Set("row", strconv.Itoa(row))
		page := read()
		if page.Reference.Rows != 151 || len(page.Rows) == 0 || len(page.Rows) > 64 {
			t.Fatalf("invalid row page: rows=%d total=%d", len(page.Rows), page.Reference.Rows)
		}
		for _, value := range page.Rows {
			if value.Index != row {
				t.Fatalf("row gap: got %d want %d", value.Index, row)
			}
			joined.WriteString(value.Text)
			row++
		}
		if page.Complete {
			if page.EndOffset != ref.TotalRunes {
				t.Fatalf("end=%d want=%d", page.EndOffset, ref.TotalRunes)
			}
			break
		}
	}
	if joined.String() != body {
		t.Fatal("row pages changed recorded content")
	}
	query.Del("row")
	query.Set("locate", strconv.Itoa(ref.TotalRunes-1))
	last := read()
	if len(last.Rows) != 1 || last.Rows[0].Text != "last line" || !last.Complete {
		t.Fatalf("locate did not return the last natural line: %+v", last.Rows)
	}
}
