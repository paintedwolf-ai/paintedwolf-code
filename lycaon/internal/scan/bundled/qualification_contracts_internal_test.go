package bundled

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
)

func TestContractTranslationRequiresBothSuccessfulPhases(t *testing.T) {
	for _, phase := range []string{"discovery", "translation"} {
		for _, failure := range []string{"missing", "timed_out", "returncode"} {
			t.Run(phase+"/"+failure, func(t *testing.T) {
				good := map[string]any{"timed_out": false, "returncode": 0}
				phases := map[string]any{"discovery": good, "translation": good}
				if failure == "missing" {
					delete(phases, phase)
				} else {
					bad := map[string]any{"timed_out": false, "returncode": 0}
					if failure == "timed_out" {
						bad["timed_out"] = true
					} else {
						bad["returncode"] = 2
					}
					phases[phase] = bad
				}
				row := map[string]any{"case": "rule-translation/tainting/example.py", "passed": true, "exit_code": 0, "execution": good, "translation_execution": phases}
				raw, err := json.Marshal(row)
				testutil.FailErr(t, "encode translation fixture", err)
				raw = append(raw, []byte("\n{\"contracts\":1,\"failed\":0}\n")...)
				filename := filepath.Join(t.TempDir(), "contracts.jsonl")
				testutil.FailErr(t, "write translation report", os.WriteFile(filename, raw, 0o600))
				if err := verifyContractReport(filename, map[string]bool{"rule-translation/tainting/example.py": true}); err == nil {
					t.Fatal("incomplete translation was qualified")
				}
			})
		}
	}
}
