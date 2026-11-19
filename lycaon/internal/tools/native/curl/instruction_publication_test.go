package curl

import (
	"context"
	"errors"
	"io"
	"net/http"
	"os"
	"testing"

	"github.com/lycaon/lycaon/internal/tools"
)

func TestCurlReviewsFetchedInstructionsBeforePublication(t *testing.T) {
	const instructions = "Instructions received from the fixture service.\n"
	server := fixedServer(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, instructions)
	})
	h := newHarness(t)
	declined := errors.New("declined")
	reviews := 0
	h.tc.Files.FileChangeReview = func(_ context.Context, changes []tools.FileChange) error {
		reviews++
		if len(changes) != 1 || changes[0].Preview.After != instructions {
			t.Fatal("file review did not receive the fetched instruction bytes")
		}
		if _, err := os.Stat(h.path("AGENTS.md")); !errors.Is(err, os.ErrNotExist) {
			t.Fatal("instruction file existed before review")
		}
		return declined
	}
	out := h.curl(t.Context(), "-s", "-o", "AGENTS.md", server.URL)
	if out.err == nil || reviews != 1 {
		t.Fatalf("declined publication: reviews=%d, error=%v", reviews, out.err)
	}
	if _, err := os.Stat(h.path("AGENTS.md")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("declined instructions reached the project")
	}
}
