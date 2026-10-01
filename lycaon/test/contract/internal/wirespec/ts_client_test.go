package wirespec

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
)

func TestClientMethodsFollowCapabilityImports(t *testing.T) {
	root := t.TempDir()
	files := map[string]string{
		"client.ts": `import type { First as Primary, Second } from "./families.ts";
export interface LycaonClient extends
  Primary,
  Second {
  own(options: { nested(): void; text: "}" }): Promise<void>;
}`,
		"families.ts": `interface Shared { shared(): void; }
export interface First extends Shared { first(): void; }
export interface Second extends Shared { second(): void; }`,
	}
	for name, body := range files {
		testutil.FailErr(t, "write client contract fixture", os.WriteFile(filepath.Join(root, name), []byte(body), 0o600))
	}
	methods, err := ParseTSClientMethods(filepath.Join(root, "client.ts"))
	testutil.FailErr(t, "parse composed client", err)
	want := map[string]struct{}{"own": {}, "first": {}, "second": {}, "shared": {}}
	if !reflect.DeepEqual(methods, want) {
		t.Fatalf("client methods = %v, want %v", methods, want)
	}
}

func TestClientMethodsRejectMissingOrCyclicBases(t *testing.T) {
	for name, body := range map[string]string{
		"missing": `export interface LycaonClient extends Missing {}`,
		"cycle":   `export interface LycaonClient extends Parent {} interface Parent extends LycaonClient {}`,
		"syntax":  `export interface LycaonClient { broken(: void; }`,
	} {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "client.ts")
			testutil.FailErr(t, "write invalid client fixture", os.WriteFile(path, []byte(body), 0o600))
			if methods, err := ParseTSClientMethods(path); err == nil {
				t.Fatalf("accepted invalid interface with methods %v", methods)
			}
		})
	}
}
