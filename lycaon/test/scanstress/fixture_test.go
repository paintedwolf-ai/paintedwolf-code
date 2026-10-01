//go:build scanstress

package scanstress

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/configdir"
	"github.com/lycaon/lycaon/internal/configlayout"
	"github.com/lycaon/lycaon/internal/confine"
	"github.com/lycaon/lycaon/internal/scan/bundled"
	scancatalog "github.com/lycaon/lycaon/internal/scan/catalog"
	bundleddriver "github.com/lycaon/lycaon/internal/scan/drivers/bundled"
	"github.com/lycaon/lycaon/internal/testutil"
)

// newScanner builds the maintained engine driver exactly as the sidecar does.
func newScanner(t *testing.T, id string, policy scancatalog.RuntimePolicy) *bundleddriver.OpenGrepScanner {
	t.Helper()
	confine.TestingSetAutoConfine(t)
	manifest, err := bundled.LoadManifest()
	testutil.FailErr(t, "load bundled manifest", err)
	home, err := configdir.UserConfigDir()
	testutil.FailErr(t, "user config dir", err)
	bin, err := bundled.ResolveOpenGrepBinary(manifest, home, configlayout.EngineRoot())
	testutil.FailErr(t, "resolve maintained engine", err)
	reportf(t, "engine binary=%s version=%s", bin, manifest.OpenGrep.Version)
	return bundleddriver.NewOpenGrepScanner(bundleddriver.OpenGrepOptions{
		ID:            id,
		HomeDir:       home,
		Manifest:      manifest,
		RuntimePolicy: policy,
	})
}

// corpusSpec sizes a generated project.
type corpusSpec struct {
	Files       int
	VulnPerFile int
}

// writeCorpus generates a project whose files each carry real matches so the
// engine does parsing, matching, and reporting work rather than skipping.
func writeCorpus(t *testing.T, spec corpusSpec) string {
	t.Helper()
	dir := t.TempDir()
	for i := 0; i < spec.Files; i++ {
		var body string
		name := ""
		switch i % 4 {
		case 0:
			name = fmt.Sprintf("mod_%03d.py", i)
			body = pythonSource(i, spec.VulnPerFile)
		case 1:
			name = fmt.Sprintf("mod_%03d.js", i)
			body = javascriptSource(i, spec.VulnPerFile)
		case 2:
			name = fmt.Sprintf("mod_%03d.ts", i)
			body = typescriptSource(i, spec.VulnPerFile)
		case 3:
			name = fmt.Sprintf("comp_%03d.vue", i)
			body = vueSource(i, spec.VulnPerFile)
		}
		testutil.FailErr(t, "write "+name, os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600))
	}
	return dir
}

func pythonSource(seed, vulns int) string {
	out := "import os\nimport subprocess\nimport hashlib\n\n"
	for i := 0; i < vulns; i++ {
		out += fmt.Sprintf(`
def handler_%d_%d(user_input):
    digest = hashlib.md5(user_input.encode()).hexdigest()
    result = eval(user_input)
    subprocess.call("echo " + user_input, shell=True)
    os.system("ls " + user_input)
    return digest, result

class Service_%d_%d:
    def process(self, payload):
        for item in payload:
            if isinstance(item, str):
                exec(item)
        return len(payload)
`, seed, i, seed, i)
	}
	return out
}

func javascriptSource(seed, vulns int) string {
	out := "'use strict';\nconst crypto = require('crypto');\n\n"
	for i := 0; i < vulns; i++ {
		out += fmt.Sprintf(`
function render_%d_%d(req, res) {
  const raw = req.query.value;
  document.getElementById('out').innerHTML = raw;
  eval(raw);
  const hash = crypto.createHash('md5').update(raw).digest('hex');
  res.write(hash);
}

const compute_%d_%d = (input) => {
  const parts = String(input).split(',').map((p) => p.trim());
  return parts.reduce((acc, p) => acc + p.length, 0);
};
`, seed, i, seed, i)
	}
	return out
}

func typescriptSource(seed, vulns int) string {
	out := "export interface Payload { value: string }\n\n"
	for i := 0; i < vulns; i++ {
		out += fmt.Sprintf(`
export function unsafe_%d_%d(payload: Payload): string {
  const code: string = payload.value;
  eval(code);
  const el = document.createElement('div');
  el.innerHTML = code;
  return el.outerHTML;
}

export class Handler_%d_%d {
  private readonly cache = new Map<string, number>();
  public size(input: Payload): number {
    this.cache.set(input.value, input.value.length);
    return this.cache.size;
  }
}
`, seed, i, seed, i)
	}
	return out
}

func vueSource(seed, vulns int) string {
	out := "<template><div id=\"app\"></div></template>\n<script setup lang=\"ts\">\n"
	for i := 0; i < vulns; i++ {
		out += fmt.Sprintf(`
const raw_%d_%d: string = location.hash;
eval(raw_%d_%d);
`, seed, i, seed, i)
	}
	out += "</script>\n"
	return out
}
