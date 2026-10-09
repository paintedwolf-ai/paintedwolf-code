package sourcecontracts

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	contractfixture "github.com/lycaon/lycaon/internal/api/contractfixture"
	"github.com/lycaon/lycaon/internal/sourceledger"
	"github.com/lycaon/lycaon/internal/testutil"
	wire "github.com/lycaon/lycaon/pkg/api"
)

func TestProjectSourceSeenListsALookAndMarkUnseenWithdrawsIt(t *testing.T) {
	ledger, ledgerDB, withLedger := contractfixture.TestSourceLedger(t)
	srv := contractfixture.NewTestServer(t, withLedger)
	p := contractfixture.CreateProjectForTest(t, srv, t.TempDir())
	contractfixture.MirrorLedgerProject(t, ledgerDB, p)
	rootID := p.Roots[0].ID

	testutil.FailErr(t, "record outside change", ledger.Record(t.Context(), sourceledger.RecordInput{
		ProjectID: p.ID, RootID: rootID, Path: "a.go",
		Op: wire.SourceChangeOpCreate, Origin: wire.SourceChangeOriginExternal,
		After: []byte("first\n"),
	}))
	var effectID, fileID string
	var ordinal int64
	testutil.FailErr(t, "read outside change", ledgerDB.QueryRowContext(t.Context(), `
		SELECT id, file_id, ordinal FROM source_effects WHERE project_id = ?
		ORDER BY ordinal DESC LIMIT 1
	`, p.ID).Scan(&effectID, &fileID, &ordinal))

	serve := func(method, target string, body []byte) *httptest.ResponseRecorder {
		t.Helper()
		w := httptest.NewRecorder()
		srv.ServeHTTP(w, contractfixture.NewAuthedRequest(method, target, bytes.NewReader(body)))
		return w
	}
	base := "/v1/projects/" + p.ID + "/source"

	body, err := json.Marshal(wire.SourcePresentationCompletion{FileID: fileID, EffectID: effectID, Ordinal: ordinal})
	testutil.FailErr(t, "encode presentation", err)
	if w := serve(http.MethodPost, base+"/seen", body); w.Code != http.StatusNoContent {
		t.Fatalf("complete outside change: status = %d body = %s", w.Code, w.Body.String())
	}

	w := serve(http.MethodGet, base+"/seen", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("seen: status = %d body = %s", w.Code, w.Body.String())
	}
	var list wire.SourceSeenList
	testutil.FailErr(t, "decode seen", json.Unmarshal(w.Body.Bytes(), &list))
	if len(list.Files) != 1 || list.NextCursor != "" || list.Files[0].FileID != fileID ||
		list.Files[0].ThroughOrdinal != ordinal || len(list.Files[0].Effects) != 1 {
		t.Fatalf("seen = %+v, want a.go through %d with its one change", list, ordinal)
	}

	withdraw := func(query string) int {
		t.Helper()
		return serve(http.MethodDelete, base+"/seen/"+fileID+query, nil).Code
	}
	for _, tc := range []struct {
		name, query string
		want        int
	}{
		{"missing look", "", http.StatusBadRequest},
		{"a look that was replaced", fmt.Sprintf("?through_ordinal=%d", ordinal+100), http.StatusConflict},
		{"the listed look", fmt.Sprintf("?through_ordinal=%d", ordinal), http.StatusNoContent},
		{"nothing left to withdraw", fmt.Sprintf("?through_ordinal=%d", ordinal), http.StatusNotFound},
	} {
		if got := withdraw(tc.query); got != tc.want {
			t.Fatalf("%s: status = %d, want %d", tc.name, got, tc.want)
		}
	}

	w = serve(http.MethodGet, base+"/seen", nil)
	testutil.FailErr(t, "decode seen after withdraw", json.Unmarshal(w.Body.Bytes(), &list))
	if len(list.Files) != 0 {
		t.Fatalf("seen after withdraw = %+v, want none", list.Files)
	}
	if w := serve(http.MethodGet, base+"/seen?limit=101", nil); w.Code != http.StatusBadRequest {
		t.Fatalf("oversized limit: status = %d, want 400", w.Code)
	}
}

func TestProjectSourceSeenPagesEveryLookOnce(t *testing.T) {
	ledger, ledgerDB, withLedger := contractfixture.TestSourceLedger(t)
	srv := contractfixture.NewTestServer(t, withLedger)
	p := contractfixture.CreateProjectForTest(t, srv, t.TempDir())
	contractfixture.MirrorLedgerProject(t, ledgerDB, p)
	base := "/v1/projects/" + p.ID + "/source/seen"
	serve := func(method, target string, body []byte) *httptest.ResponseRecorder {
		t.Helper()
		w := httptest.NewRecorder()
		srv.ServeHTTP(w, contractfixture.NewAuthedRequest(method, target, bytes.NewReader(body)))
		return w
	}

	for _, path := range []string{"a.go", "b.go", "c.go"} {
		testutil.FailErr(t, "record "+path, ledger.Record(t.Context(), sourceledger.RecordInput{
			ProjectID: p.ID, RootID: p.Roots[0].ID, Path: path,
			Op: wire.SourceChangeOpCreate, Origin: wire.SourceChangeOriginExternal, After: []byte(path),
		}))
		var look wire.SourcePresentationCompletion
		testutil.FailErr(t, "read "+path, ledgerDB.QueryRowContext(t.Context(), `
			SELECT id, file_id, ordinal FROM source_effects WHERE project_id = ?
			ORDER BY ordinal DESC LIMIT 1
		`, p.ID).Scan(&look.EffectID, &look.FileID, &look.Ordinal))
		body, err := json.Marshal(look)
		testutil.FailErr(t, "encode look", err)
		if w := serve(http.MethodPost, base, body); w.Code != http.StatusNoContent {
			t.Fatalf("look at %s: status = %d body = %s", path, w.Code, w.Body.String())
		}
	}

	listed := map[string]bool{}
	cursor := ""
	for pages := 0; ; pages++ {
		if pages > 3 {
			t.Fatal("seen paging did not terminate")
		}
		target := base + "?limit=1"
		if cursor != "" {
			target += "&cursor=" + url.QueryEscape(cursor)
		}
		w := serve(http.MethodGet, target, nil)
		if w.Code != http.StatusOK {
			t.Fatalf("seen page: status = %d body = %s", w.Code, w.Body.String())
		}
		var page wire.SourceSeenList
		testutil.FailErr(t, "decode seen page", json.Unmarshal(w.Body.Bytes(), &page))
		for _, file := range page.Files {
			if listed[file.FileID] {
				t.Fatalf("file %s listed twice", file.FileID)
			}
			listed[file.FileID] = true
		}
		if page.NextCursor == "" {
			break
		}
		if cursor == "" {
			// A cursor continues only the listing that issued it.
			other := serve(http.MethodGet, base+"?mark_user_edits=false&cursor="+url.QueryEscape(page.NextCursor), nil)
			if other.Code != http.StatusBadRequest {
				t.Fatalf("cursor on another listing: status = %d body = %s", other.Code, other.Body.String())
			}
		}
		cursor = page.NextCursor
	}
	if len(listed) != 3 {
		t.Fatalf("paged %d seen files, want 3", len(listed))
	}
	if w := serve(http.MethodGet, base+"?cursor="+url.QueryEscape("file-1"), nil); w.Code != http.StatusBadRequest {
		t.Fatalf("plaintext cursor: status = %d, want 400", w.Code)
	}
}
