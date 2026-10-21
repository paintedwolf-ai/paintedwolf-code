package pkgmanifest_test

import (
	"github.com/lycaon/lycaon/internal/pkgmanifest"
	"testing"
)

func payload(m map[string]any) []map[string]any { return m["payload"].([]map[string]any) }

func TestPayloadManifestRejectsIncompleteOrUnsafeGenerationInventory(t *testing.T) {
	cases := map[string]func(map[string]any){
		"absent inventory":        func(m map[string]any) { delete(m, "payload") },
		"empty inventory":         func(m map[string]any) { m["payload"] = []map[string]any{} },
		"missing explicit field":  func(m map[string]any) { delete(payload(m)[1], "target") },
		"foreign field":           func(m map[string]any) { payload(m)[1]["authority"] = true },
		"escaping path":           func(m map[string]any) { payload(m)[1]["path"] = "../pw" },
		"absolute path":           func(m map[string]any) { payload(m)[1]["path"] = "/bin/pw" },
		"self reference":          func(m map[string]any) { payload(m)[1]["path"] = "manifest.json" },
		"writable image":          func(m map[string]any) { payload(m)[1]["mode"] = 0777 },
		"setgid image":            func(m map[string]any) { payload(m)[1]["mode"] = 02755 },
		"missing physical parent": func(m map[string]any) { m["payload"] = payload(m)[1:] },
		"directory as image":      func(m map[string]any) { payload(m)[1]["kind"] = "directory" },
		"image content differs":   func(m map[string]any) { payload(m)[1]["size"] = 0 },
		"duplicate path":          func(m map[string]any) { m["payload"] = append(payload(m), payload(m)[0]) },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			file, _ := fixture(t, mutate)
			if _, err := pkgmanifest.Load(file); err == nil {
				t.Fatal("accepted unsafe payload inventory")
			}
		})
	}
}

func TestPayloadLinksStayInsideOneCompleteGeneration(t *testing.T) {
	for _, target := range []string{"../bin/pw", "../bin", ".."} {
		t.Run(target, func(t *testing.T) {
			file, _ := fixture(t, func(m map[string]any) {
				m["payload"] = append(payload(m), map[string]any{"path": "bin/alias", "kind": "symlink", "mode": 0777, "sha256": nil, "size": nil, "target": target})
			})
			if _, err := pkgmanifest.Load(file); err != nil {
				t.Fatal(err)
			}
		})
	}
	for _, target := range []string{"../../escape", "/nix/store/arbitrary/bin/pw", "missing", "alias"} {
		t.Run(target, func(t *testing.T) {
			file, _ := fixture(t, func(m map[string]any) {
				m["payload"] = append(payload(m), map[string]any{"path": "bin/alias", "kind": "symlink", "mode": 0777, "sha256": nil, "size": nil, "target": target})
			})
			if _, err := pkgmanifest.Load(file); err == nil {
				t.Fatal("accepted escaping, dangling or cyclic link")
			}
		})
	}
}
