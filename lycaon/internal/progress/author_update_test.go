package progress

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
)

func TestPreparedAuthorUpdateHasNoEffectUntilApply(t *testing.T) {
	store := NewMemoryStore()
	content := "## Progress\n- [ ] Inspect source"
	prepared, err := PrepareAuthorUpdate(content)
	testutil.FailErr(t, "prepare progress", err)
	if store.Get(t.Context(), "session") != "" {
		t.Fatal("preparation committed progress")
	}
	result, err := prepared.Apply(t.Context(), store, "session")
	testutil.FailErr(t, "apply prepared progress", err)
	if !result.Updated || store.Get(t.Context(), "session") != content {
		t.Fatalf("progress=%+v content=%q", result, store.Get(t.Context(), "session"))
	}
	result, err = prepared.Apply(t.Context(), store, "session")
	testutil.FailErr(t, "reapply identical progress", err)
	if result.Updated {
		t.Fatal("identical progress created another update")
	}
}

func TestPreparedAuthorUpdateRejectsWithoutWrites(t *testing.T) {
	for _, content := range []string{"", "- [ ] outer\n  - [ ] nested", strings.Repeat("- [ ] step\n", MaxAuthorProgressLines+1)} {
		if _, err := PrepareAuthorUpdate(content); err == nil {
			t.Fatalf("invalid progress admitted %q", content)
		}
	}
	_, err := PrepareAuthorUpdate("- [ ] outer\n  - [ ] nested")
	var reject *AuthorUpdateRejection
	if !errors.As(err, &reject) || reject.Code != "PROGRESS_NESTED_LINE" {
		t.Fatalf("lost owner rejection: %v", err)
	}
	prepared, err := PrepareAuthorUpdate("- [ ] work")
	testutil.FailErr(t, "prepare valid progress", err)
	store := NewMemoryStore()
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := prepared.Apply(ctx, store, "session"); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled apply: %v", err)
	}
	if _, err := prepared.Apply(t.Context(), store, ""); err == nil {
		t.Fatal("missing session admitted")
	}
	if _, err := (AuthorUpdate{}).Apply(t.Context(), store, "session"); err == nil {
		t.Fatal("unprepared update admitted")
	}
	if store.Get(t.Context(), "session") != "" {
		t.Fatal("refused update committed")
	}
}

func TestPreparedAuthorUpdateUsesTheAppliedSessionHistory(t *testing.T) {
	prepared, err := PrepareAuthorUpdate("## Progress\n- [x] Inspect source")
	testutil.FailErr(t, "prepare completed checklist", err)
	store := NewMemoryStore()
	var rejected *AuthorUpdateRejection
	if _, err := prepared.Apply(t.Context(), store, "session"); !errors.As(err, &rejected) || rejected.Code != "PROGRESS_NO_OPEN_STEP" {
		t.Fatalf("initial completion rejection: %v", err)
	}
	if got := store.Get(t.Context(), "session"); got != "" {
		t.Fatalf("refused initial completion wrote %q", got)
	}
	testutil.FailErr(t, "install existing open checklist", store.Set("session", "## Progress\n- [ ] Inspect source"))
	result, err := prepared.Apply(t.Context(), store, "session")
	testutil.FailErr(t, "complete the existing checklist", err)
	if !result.Updated || store.Get(t.Context(), "session") != "## Progress\n- [x] Inspect source" {
		t.Fatalf("completed checklist: %+v / %q", result, store.Get(t.Context(), "session"))
	}
}
