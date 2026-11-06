package sessions

import (
	"testing"

	"github.com/lycaon/lycaon/config"
	"github.com/lycaon/lycaon/config/configtest"
	"github.com/lycaon/lycaon/internal/llm"
	"github.com/lycaon/lycaon/internal/llm/compaction"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestSessionCompactorDoesNotResolveAWindow(t *testing.T) {
	configtest.Overlay(t, map[config.Rel]string{config.ModelContextWindow: "fallback_true_window: 8192\nwindows:\n  - match: [fixture-window]\n    context_window: 16384\n"})
	// The matrix only selects summarizer construction; no provider transport exists.
	for _, mock := range []string{"1", "0"} {
		t.Run(mock, func(t *testing.T) {
			t.Setenv("LYCAON_LLM_MOCK", mock)
			svc := &llm.Service{Registry: &llm.Registry{}, Policy: llm.NewInMemoryPolicyStore(llm.ModelPolicy{Coordinator: llm.ModelRef{ProviderID: "unconfigured", Model: "fixture-window"}})}
			compactor, err := loadCompactor(svc, nil)
			testutil.FailErr(t, "load session compactor", err)
			if got, want := compactor.Config().ModelContextWindow, compaction.DefaultCompactionConfig().ModelContextWindow; got != want {
				t.Fatalf("compactor window = %d, want unresolved default %d", got, want)
			}
		})
	}
	_, err := loadCompactor(nil, nil)
	testutil.FailErr(t, "load fallback compactor", err)
}

func TestSessionCompactorRefusesInvalidWindowCatalog(t *testing.T) {
	configtest.Overlay(t, map[config.Rel]string{config.ModelContextWindow: "windows: ["})
	if compactor, err := loadCompactor(nil, nil); err == nil || compactor != nil {
		t.Fatalf("invalid model window catalog accepted:%v,%v", compactor, err)
	}
}
