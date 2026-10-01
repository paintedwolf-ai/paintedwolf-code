package output

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestOpengrepEvidenceSpansUseSourceByteColumns(t *testing.T) {
	root := t.TempDir()
	testutil.FailErr(t, "write source bytes", os.WriteFile(filepath.Join(root, "source.py"), []byte("狼 = 1\r\nrun()\n"), 0o600))
	location := api.SecurityFindingLocation{URI: "source.py", StartLine: 1, StartColumn: 1, EndLine: 1, EndColumn: 4}
	result := &Result{Findings: []api.SecurityFinding{{Locations: []api.SecurityFindingLocation{location}, Dataflow: &api.SecurityFindingDataflow{
		Source: &api.SecurityFindingCallTrace{Location: location, Callee: &api.SecurityFindingCallTrace{Location: location}},
	}}}}
	testutil.FailErr(t, "validate Unicode byte span", ValidateOpengrepLocations(result, root))
	for _, invalid := range []api.SecurityFindingLocation{
		{URI: "source.py", StartLine: 1, StartColumn: 1, EndLine: 99, EndColumn: 1},
		{URI: "source.py", StartLine: 2, StartColumn: 1, EndLine: 2, EndColumn: 100},
		{URI: "source.py", StartLine: 2, StartColumn: 4, EndLine: 2, EndColumn: 2},
		{URI: "source.py", StartLine: 2, StartColumn: 1},
	} {
		result.Findings[0].Dataflow.Source.Callee.Location = invalid
		if err := ValidateOpengrepLocations(result, root); err == nil {
			t.Fatalf("accepted invalid nested evidence span: %+v", invalid)
		}
	}
}
