package textutil

import (
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
)

func TestXargsHandoffWakesOnlyAnAvailableSelection(t *testing.T) {
	t.Run("closed input cannot wake capacity wait", func(t *testing.T) {
		h := newXargsHandoff(nil)
		first := make(chan struct{})
		release, err := h.block(t.Context(), nil, first)
		testutil.FailErr(t, "h.block failed", err)
		defer release()
		h.parsed()
		if h.dispatcherUnblock == nil {
			t.Fatal("closed disabled input cleared child wait")
		}
		h.childDone(first)
		if h.dispatcherUnblock != nil {
			t.Fatal("selected child completion did not wake dispatcher")
		}
	})
	t.Run("later child cannot wake ordered join", func(t *testing.T) {
		h := newXargsHandoff(nil)
		first, later := make(chan struct{}), make(chan struct{})
		release, err := h.block(t.Context(), nil, first)
		testutil.FailErr(t, "h.block failed", err)
		defer release()
		h.childDone(later)
		if h.dispatcherUnblock == nil {
			t.Fatal("unselected child cleared ordered wait")
		}
		h.childDone(first)
		if h.dispatcherUnblock != nil {
			t.Fatal("selected child completion did not wake dispatcher")
		}
	})
	t.Run("enabled input wakes dispatcher", func(t *testing.T) {
		h := newXargsHandoff(nil)
		release, err := h.block(t.Context(), h.next, nil)
		testutil.FailErr(t, "h.block failed", err)
		defer release()
		if err := h.publish(t.Context(), nil, xargsNext{}); err != nil {
			testutil.FailErr(t, "h.publish failed", err)
		}
		if h.dispatcherUnblock != nil {
			t.Fatal("queued enabled input did not wake dispatcher")
		}
	})
}
