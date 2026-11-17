package contract

import (
	"testing"

	"github.com/lycaon/lycaon/internal/guidance"
	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
	"github.com/lycaon/lycaon/test/contract/laws/catalogload"
)

func TestSynthesisGroundingCodePairingClosure(t *testing.T) {
	t.Parallel()
	for _, synth := range catalogload.SynthesisGroundingCodes {
		worker, ok := synthesisToWorkerGroundingCodePairing[synth]
		if !ok || worker == "" {
			t.Fatalf("synthesis code %q missing worker counterpart in synthesisToWorkerGroundingCodePairing", synth)
		}
		found := false
		for _, w := range catalogload.WorkerGroundingCodes {
			if w == worker {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("paired worker code %q for %q not in catalogload.WorkerGroundingCodes", worker, synth)
		}
	}
}

func TestMissingCoordinatorCitationsWarnWithoutModelRetry(t *testing.T) {
	t.Parallel()
	cfg, err := guidance.LoadHintConfigStock()
	contractcheck.FailErr(t, "load host attachment policy", err)
	for _, code := range []string{guidance.InvestCitationsRequiredCode, guidance.SynthCitationsRequiredCode} {
		entry, ok := cfg.HintCodes[code]
		if !ok || entry.Effect != "warn" || cfg.IsInSessionRetry(code) {
			t.Fatalf("host attachment policy for %s: present=%v effect=%s retry=%v", code, ok, entry.Effect, cfg.IsInSessionRetry(code))
		}
	}
}
