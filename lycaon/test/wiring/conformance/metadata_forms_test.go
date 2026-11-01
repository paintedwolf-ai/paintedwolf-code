package conformance_test

import (
	"encoding/json"
	"slices"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/test/commandconformance"
)

var metadataFormContracts = []contractCase{
	{
		Case: commandconformance.Case{
			ID: "metadata-disk-forms", Commands: []string{"df", "du"},
			Forms:   []string{"df:-h", "df:-k", "df:-P", "df:-i", "du:-s", "du:-a", "du:-h", "du:-k", "du:-m", "du:-c", "du:-b", "du:-x", "du:-d", "du:--apparent-size"},
			Program: `for option in -h -k -P -i; do df "$option" . >/dev/null || exit 1; done; for option in -s -a -h -k -m -c -b -x; do du "$option" sub >/dev/null || exit 1; done; du -d 1 --apparent-size sub >/dev/null && test "$(du -b sub/a.txt | cut -f 1)" = 13 && printf 'disk-forms-ok\n'`,
		},
		Stdout: "disk-forms-ok\n",
	},
	{
		Case: commandconformance.Case{
			ID: "metadata-tree-forms", Commands: []string{"tree"},
			Forms:   []string{"tree:-L", "tree:-a", "tree:-d", "tree:-f", "tree:-I", "tree:-P", "tree:--gitignore", "tree:--noreport", "tree:-J"},
			Program: `tree -L 2 -a -d -f -I 'b*' -P '*.txt' --gitignore --noreport sub >/dev/null && tree -J -L 1 --noreport sub`,
		},
		Validate: verifyTreeForms,
	},
}

func verifyTreeForms(t *testing.T, obs runtimeObservation) {
	t.Helper()
	var roots []struct {
		Type     string `json:"type"`
		Name     string `json:"name"`
		Contents []struct {
			Name string `json:"name"`
		} `json:"contents"`
	}
	testutil.FailErr(t, "decode tree", json.Unmarshal([]byte(obs.Outcome.Stdout), &roots))
	if len(roots) != 1 || roots[0].Type != "directory" || roots[0].Name != "sub" || !slices.ContainsFunc(roots[0].Contents, func(entry struct {
		Name string `json:"name"`
	}) bool { return entry.Name == "a.txt" }) {
		t.Fatalf("tree = %s", obs.Outcome.Stdout)
	}
}
