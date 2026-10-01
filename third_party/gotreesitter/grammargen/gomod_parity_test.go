package grammargen

import "testing"

func TestGomodGroupedRetractIntervalParity(t *testing.T) {
	assertImportedDeepParityCases(t, "gomod", []struct {
		name string
		src  string
	}{
		{
			name: "bracketed_interval_in_group",
			src:  "retract (\n    v1.0.0\n    [v1.0.0, v1.9.9]\n)\n",
		},
	})
}
