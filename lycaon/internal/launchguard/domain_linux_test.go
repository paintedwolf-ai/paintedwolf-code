//go:build linux

package launchguard

import "testing"

func TestDomainPopulationRequiresKernelFact(t *testing.T) {
	for _, test := range []struct {
		raw  string
		want bool
	}{{"populated 0\nfrozen 0\n", false}, {"populated 1\nfrozen 0\n", true}} {
		got, err := populatedState(test.raw)
		if err != nil || got != test.want {
			t.Fatalf("decode domain events %q: populated=%v error=%v", test.raw, got, err)
		}
	}
	for _, raw := range []string{"", "frozen 0\n", "populated 2\n", "populated -1\n", "populated 0\npopulated 1\n", "populated true\n", "populated 1 extra\n"} {
		if _, err := populatedState(raw); err == nil {
			t.Errorf("accepted invalid domain population %q", raw)
		}
	}
}
