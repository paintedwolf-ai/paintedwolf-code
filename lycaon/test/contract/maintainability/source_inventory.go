package maintainability

import (
	"fmt"
	"path"
	"path/filepath"
	"slices"
	"strings"

	"gopkg.in/yaml.v3"
)

type source struct {
	Path, Language string
	Test           bool
	Body           []byte
}

// discoverSources reads the handwritten sources of a checkout.
func discoverSources(tree *workingTree) ([]source, error) {
	generated, err := generatedOutputs(tree)
	if err != nil {
		return nil, err
	}
	var sources []source
	for _, relative := range tree.files() {
		language := languageOf(relative)
		// A tracked deletion or a link is not maintained source.
		if language == "" || excludedPath(relative) || generated[relative] || !tree.regular(relative) {
			continue
		}
		body, err := tree.read(relative)
		if err != nil {
			return nil, fmt.Errorf("read maintained source %s: %w", relative, err)
		}
		sources = append(sources, source{relative, language, testPath(relative), body})
	}
	if len(sources) == 0 {
		return nil, fmt.Errorf("maintainability source inventory is empty")
	}
	return sources, nil
}

func languageOf(relative string) string {
	switch filepath.Ext(relative) {
	case ".go":
		return "go"
	case ".ts", ".mts", ".cts":
		return "typescript"
	case ".tsx":
		return "tsx"
	case ".js", ".jsx", ".mjs", ".cjs":
		return "javascript"
	case ".rs":
		return "rust"
	case ".py":
		return "python"
	case ".sh", ".bash":
		return "bash"
	}
	if relative == "task" {
		return "bash"
	}
	return ""
}

// excludedPath drops upstream material and source-shaped fixture inputs.
func excludedPath(relative string) bool {
	if strings.HasPrefix(relative, "third_party/") || strings.HasPrefix(relative, "lycaon/test/fixtures/") {
		return true
	}
	parts := strings.Split(relative, "/")
	return slices.Contains(parts, "vendor") || slices.Contains(parts, "node_modules") || slices.Contains(parts, "testdata")
}

func testPath(relative string) bool {
	for _, part := range strings.Split(relative, "/") {
		switch part {
		case "test", "tests", "e2e", "testutil", "testcorpus", "testsupport", "testfixture", "verification_tests", "__tests__":
			return true
		}
	}
	name := path.Base(relative)
	return name == "tests.rs" || strings.HasSuffix(name, "_test.go") || strings.Contains(name, ".test.") || strings.Contains(name, ".spec.") ||
		strings.HasPrefix(name, "test_") || strings.HasSuffix(name, "_test.py") || strings.Contains(name, "_testutil.") || strings.Contains(name, "_testsupport.")
}

// generatedOutputs trusts generator declarations, never filenames or
// self-declared headers, so handwritten code cannot opt out of its budget.
func generatedOutputs(tree *workingTree) (map[string]bool, error) {
	var taskfile struct {
		Tasks map[string]struct {
			Dir       string
			Generates []string
		}
	}
	raw, err := tree.read("Taskfile.yml")
	if err == nil {
		err = yaml.Unmarshal(raw, &taskfile)
	}
	if err != nil {
		return nil, fmt.Errorf("read generator declarations: %w", err)
	}
	out := map[string]bool{}
	for name, task := range taskfile.Tasks {
		if !strings.HasPrefix(name, "codegen:") && name != "db:sqlc" {
			continue
		}
		dir := ""
		switch task.Dir {
		case "":
		case "{{.GO_DIR}}":
			dir = "lycaon"
		default:
			return nil, fmt.Errorf("unrecognized generator directory %q for %s", task.Dir, name)
		}
		for _, pattern := range task.Generates {
			if strings.Contains(pattern, "{{") {
				return nil, fmt.Errorf("unresolved generator output %s: %s", name, pattern)
			}
			matches, err := glob(tree, path.Join(dir, pattern))
			if err != nil {
				return nil, fmt.Errorf("expand generator output %s: %w", pattern, err)
			}
			for _, match := range matches {
				out[match] = true
			}
		}
	}
	// codegen:wire-enums derives its outputs from the vocabulary files.
	vocabulary, err := glob(tree, "docs/openapi/vocab/*.yaml")
	if err != nil {
		return nil, err
	}
	for _, file := range vocabulary {
		var entry struct {
			Name         string
			GoFile       string `yaml:"go_file"`
			StateMachine *struct {
				TSFile string `yaml:"ts_file"`
			} `yaml:"state_machine"`
			TSLabels *struct{ Path string } `yaml:"ts_labels"`
		}
		raw, err := tree.read(file)
		if err == nil {
			err = yaml.Unmarshal(raw, &entry)
		}
		if err != nil {
			return nil, fmt.Errorf("read wire vocabulary %s: %w", file, err)
		}
		if entry.GoFile == "" {
			var snake strings.Builder
			for i, r := range entry.Name {
				if r >= 'A' && r <= 'Z' {
					if i > 0 {
						snake.WriteByte('_')
					}
					r += 'a' - 'A'
				}
				snake.WriteRune(r)
			}
			entry.GoFile = snake.String() + "_ids.generated.go"
		}
		out["lycaon/pkg/api/"+entry.GoFile] = true
		if entry.StateMachine != nil {
			out[entry.StateMachine.TSFile] = true
		}
		if entry.TSLabels != nil {
			out[entry.TSLabels.Path] = true
		}
		if entry.Name == "EventTopic" {
			out["lycaon-den/src/api/event-topics.generated.ts"] = true
			out["lycaon-den/src/api/event-payloads.generated.ts"] = true
		}
	}
	return out, nil
}
