package security

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/testutil"
	wire "github.com/lycaon/lycaon/pkg/api"
	"github.com/lycaon/lycaon/test/wiring"
)

func TestBoardRepoBriefPopulated(t *testing.T) {
	h := wiring.BuildForTest(t)
	srv := h.Server
	projectDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(projectDir, "main.go"), []byte("package main\n\nfunc main() {}\n"), 0o644); err != nil {
		testutil.FailErr(t, "write file", err)
	}

	p := createProjectHTTP(t, srv, projectDir)
	sess := createSessionForProjectHTTP(t, srv, p.ID, wire.SessionPostureBuild)

	var board wire.BoardView
	var boardBody string
	testutil.WaitFor(t, 5*time.Second, func() bool {
		req := authedRequest(t, http.MethodGet, "/v1/projects/"+p.ID+"/board?session_id="+sess.ID, nil)
		w := httptest.NewRecorder()
		srv.ServeHTTP(w, req)
		if w.Code != http.StatusOK {
			t.Fatalf("GET board status = %d body = %s", w.Code, w.Body.String())
		}
		if err := json.Unmarshal(w.Body.Bytes(), &board); err != nil {
			testutil.FailErr(t, "unmarshal JSON document", err)
		}
		boardBody = w.Body.String()
		return board.Summary != "" && board.Repo.FileCount > 0
	})
	if len(board.Repo.Languages) == 0 {
		t.Fatalf("board repo languages empty: %+v", board.Repo)
	}
	if strings.Contains(boardBody, "workspace_baseline_path") {
		t.Fatal("compact board must not include workspace_baseline_path")
	}
}
