package renderhandle

import (
	"errors"
	"fmt"
	"testing"

	"github.com/lycaon/lycaon/internal/browser"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestStoreCRUD(t *testing.T) {
	s := NewStore()
	if _, ok := s.Get("sess-1", "handle-a"); ok {
		t.Fatal("expected handle-a to not exist")
	}
	stored, err := s.Put("sess-1", &RenderHandle{
		ID:     "handle-a",
		Markup: "<div>Initial</div>",
		Mime:   "text/html",
		Canvas: browser.RenderCanvas{Width: 800, Height: 600},
	}, 0)
	testutil.FailErr(t, "put", err)
	if stored.Revision != 1 {
		t.Fatalf("revision = %d, want 1", stored.Revision)
	}
	got, ok := s.Get("sess-1", "handle-a")
	if !ok || got.Markup != "<div>Initial</div>" {
		t.Fatalf("get = %+v, %v", got, ok)
	}
	if _, ok := s.Get("sess-2", "handle-a"); ok {
		t.Fatal("expected handle-a not to exist in sess-2")
	}
	updated := got.Clone()
	updated.Markup = "<div>Updated</div>"
	res, err := s.Put("sess-1", updated, 0)
	testutil.FailErr(t, "replace", err)
	if res.Revision != 2 || res.Markup != "<div>Updated</div>" {
		t.Fatalf("replace = %+v", res)
	}
	if list := s.List("sess-1"); len(list) != 1 || list[0].ID != "handle-a" {
		t.Fatalf("list = %+v", list)
	}
	s.Release("sess-1")
	if _, ok := s.Get("sess-1", "handle-a"); ok {
		t.Fatal("released session still holds handles")
	}
}

// Patch computes; only the Put after a successful render commits, so one
// patch is one revision and a failed render leaves the markup unchanged.
func TestStorePatchCommitsOnce(t *testing.T) {
	s := NewStore()
	_, err := s.Put("sess-1", &RenderHandle{ID: "card", Markup: `<div class="bg-blue">Hello</div>`}, 0)
	testutil.FailErr(t, "seed", err)

	patched, err := s.Patch("sess-1", "card", "bg-blue", "bg-red", false)
	testutil.FailErr(t, "patch", err)
	if patched.Markup != `<div class="bg-red">Hello</div>` || patched.Revision != 1 {
		t.Fatalf("patched = %+v", patched)
	}
	if current, _ := s.Get("sess-1", "card"); current.Markup != `<div class="bg-blue">Hello</div>` || current.Revision != 1 {
		t.Fatalf("patch mutated the store before render: %+v", current)
	}
	committed, err := s.Put("sess-1", patched, patched.Revision)
	testutil.FailErr(t, "commit", err)
	if committed.Revision != 2 {
		t.Fatalf("revision after one patch = %d, want 2", committed.Revision)
	}
}

func TestStorePutRejectsStaleBase(t *testing.T) {
	s := NewStore()
	_, err := s.Put("sess-1", &RenderHandle{ID: "card", Markup: "a b"}, 0)
	testutil.FailErr(t, "seed", err)
	first, err := s.Patch("sess-1", "card", "a", "A", false)
	testutil.FailErr(t, "first patch", err)
	second, err := s.Patch("sess-1", "card", "b", "B", false)
	testutil.FailErr(t, "second patch", err)
	_, err = s.Put("sess-1", first, first.Revision)
	testutil.FailErr(t, "first commit", err)
	if _, err := s.Put("sess-1", second, second.Revision); !errors.Is(err, ErrHandleConflict) {
		t.Fatalf("stale commit err = %v, want ErrHandleConflict", err)
	}
	if got, _ := s.Get("sess-1", "card"); got.Markup != "A b" {
		t.Fatalf("stale commit overwrote markup: %q", got.Markup)
	}
}

func TestStorePatchErrors(t *testing.T) {
	s := NewStore()
	_, err := s.Put("sess-1", &RenderHandle{ID: "card", Markup: `<span>item</span><span>item</span>`}, 0)
	testutil.FailErr(t, "seed", err)
	if _, err := s.Patch("sess-1", "card", "nonexistent", "x", false); !errors.Is(err, ErrPatchNotFound) {
		t.Fatalf("not found err = %v", err)
	}
	if _, err := s.Patch("sess-1", "card", "item", "entry", false); !errors.Is(err, ErrPatchAmbiguous) {
		t.Fatalf("ambiguous err = %v", err)
	}
	all, err := s.Patch("sess-1", "card", "item", "entry", true)
	testutil.FailErr(t, "replace all", err)
	if all.Markup != `<span>entry</span><span>entry</span>` {
		t.Fatalf("replace all = %q", all.Markup)
	}
	if _, err := s.Patch("sess-1", "card", "", "foo", false); !errors.Is(err, ErrPatchEmptyTarget) {
		t.Fatalf("empty target err = %v", err)
	}
	if _, err := s.Patch("sess-1", "ghost", "foo", "bar", false); !errors.Is(err, ErrHandleNotFound) {
		t.Fatalf("ghost err = %v", err)
	}
}

func TestStoreEvictsLeastRecentlyUsed(t *testing.T) {
	s := newStore(2, 10)
	for i := 0; i < 2; i++ {
		_, err := s.Put("sess", &RenderHandle{ID: fmt.Sprintf("h%d", i), Bytes: []byte("xx")}, 0)
		testutil.FailErr(t, "put", err)
	}
	s.Get("sess", "h0")
	_, err := s.Put("sess", &RenderHandle{ID: "h2", Bytes: []byte("xx")}, 0)
	testutil.FailErr(t, "put over count", err)
	if _, ok := s.Get("sess", "h1"); ok {
		t.Fatal("least recently used handle survived the count bound")
	}
	if _, ok := s.Get("sess", "h0"); !ok {
		t.Fatal("recently read handle was evicted")
	}

	_, err = s.Put("sess", &RenderHandle{ID: "big", Bytes: make([]byte, 9)}, 0)
	testutil.FailErr(t, "put over bytes", err)
	if list := s.List("sess"); len(list) != 1 || list[0].ID != "big" {
		t.Fatalf("byte bound kept %d handles", len(list))
	}
}
