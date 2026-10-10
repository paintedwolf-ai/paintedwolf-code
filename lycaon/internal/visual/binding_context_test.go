package visual

import (
	"context"
	"testing"
)

type bindingContextKey struct{}
type bindingContextStore struct {
	*MemoryStore
	observed any
}

func (s *bindingContextStore) BindEvidenceHandle(ctx context.Context, _, _ string) error {
	s.observed = ctx.Value(bindingContextKey{})
	return nil
}

func TestCommittedEvidenceBindingPreservesContext(t *testing.T) {
	hot := &bindingContextStore{MemoryStore: NewMemoryStore()}
	records := &Records{}
	NewDurableStore(DurableConfig{DataDir: t.TempDir(), Hot: hot, Records: records})
	records.EvidenceHandleBound(context.WithValue(t.Context(), bindingContextKey{}, "committed-binding"), "artifact", "evidence")
	if hot.observed != "committed-binding" {
		t.Fatalf("hot binding context=%v", hot.observed)
	}
}
