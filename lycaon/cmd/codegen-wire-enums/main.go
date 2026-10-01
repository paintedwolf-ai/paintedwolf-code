// Command codegen-wire-enums generates wire vocabularies from docs/openapi/vocab.
package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

func main() {
	repoRoot := flag.String("repo-root", ".", "repository root (contains docs/openapi/vocab)")
	check := flag.Bool("check", false, "exit 1 if any output is stale")
	stageDir := flag.String("stage-dir", "", "write outputs under this directory plus a manifest of repo-relative paths; never touches the repository")
	flag.Parse()

	absRoot, err := filepath.Abs(*repoRoot)
	if err != nil {
		fatal(err)
	}
	vocabDir := filepath.Join(absRoot, "docs", "openapi", "vocab")
	enums, err := loadVocabDir(vocabDir)
	if err != nil {
		fatal(err)
	}
	if len(enums) == 0 {
		fatal(fmt.Errorf("no enum YAML under %s", vocabDir))
	}

	var outputs []outputFile
	for _, e := range enums {
		openapiPath := filepath.Join(absRoot, "docs", "openapi", "components", "schemas", e.openAPIFileName())
		openapiBody, err := renderOpenAPI(e)
		if err != nil {
			fatal(err)
		}
		outputs = append(outputs, outputFile{path: openapiPath, body: openapiBody, hint: "OpenAPI " + e.Name})

		goPath := filepath.Join(absRoot, "lycaon", "pkg", "api", e.goFileName())
		goBody, err := renderGo(e)
		if err != nil {
			fatal(err)
		}
		outputs = append(outputs, outputFile{path: goPath, body: goBody, hint: "pkg/api " + e.Name})

		if e.JSONSchema != "" {
			jsonPath := filepath.Join(absRoot, filepath.FromSlash(e.JSONSchema))
			jsonBody, err := renderJSONSchema(e)
			if err != nil {
				fatal(err)
			}
			outputs = append(outputs, outputFile{path: jsonPath, body: jsonBody, hint: "JSON Schema " + e.Name})
		}

		if e.StateMachine != nil {
			sqlPath := filepath.Join(absRoot, filepath.FromSlash(e.StateMachine.SQLCheck.Path))
			source, err := os.ReadFile(sqlPath) // #nosec G304 -- repository policy path
			if err != nil {
				fatal(err)
			}
			sqlBody, err := renderSQLCheck(e, source)
			if err != nil {
				fatal(err)
			}
			outputs = append(outputs, outputFile{path: sqlPath, body: sqlBody, hint: "SQL " + e.Name})
			tsPath := filepath.Join(absRoot, filepath.FromSlash(e.StateMachine.TSFile))
			outputs = append(outputs, outputFile{path: tsPath, body: renderTSStateMachine(e), hint: "Den " + e.Name + " state machine"})
		}

		if e.TSLabels != nil {
			labelPath := filepath.Join(absRoot, filepath.FromSlash(e.TSLabels.Path))
			labelBody, err := renderLabelsTS(e)
			if err != nil {
				fatal(err)
			}
			outputs = append(outputs, outputFile{path: labelPath, body: labelBody, hint: "Den " + e.Name + " labels"})
		}
	}

	if et, ok := findEnum(enums, "EventTopic"); ok {
		tsPath := filepath.Join(absRoot, "lycaon-den", "src", "api", "event-topics.generated.ts")
		tsBody, err := renderEventTopicsTS(et)
		if err != nil {
			fatal(err)
		}
		outputs = append(outputs, outputFile{path: tsPath, body: tsBody, hint: "Den EventTopic list"})

		payloadsPath := filepath.Join(absRoot, "lycaon-den", "src", "api", "event-payloads.generated.ts")
		payloadsBody, err := renderEventPayloadsTS(et)
		if err != nil {
			fatal(err)
		}
		outputs = append(outputs, outputFile{path: payloadsPath, body: payloadsBody, hint: "Den event payload map"})

		envelopePath := filepath.Join(absRoot, "docs", "openapi", "components", "schemas", "event-envelope.generated.yaml")
		envelopeBody, err := renderEventEnvelopeOpenAPI(et)
		if err != nil {
			fatal(err)
		}
		outputs = append(outputs, outputFile{path: envelopePath, body: envelopeBody, hint: "OpenAPI event envelope"})

		for _, topic := range et.Values {
			path := filepath.Join(absRoot, "docs", "schemas", "events", topic.ID+".json")
			body, err := renderEventPayloadJSONSchema(topic)
			if err != nil {
				fatal(err)
			}
			outputs = append(outputs, outputFile{path: path, body: body, hint: "JSON Schema event payload " + topic.ID})
		}
		envelopeJSONPath := filepath.Join(absRoot, "docs", "schemas", "events", "envelope.json")
		envelopeJSONBody, err := renderEventEnvelopeJSONSchema()
		if err != nil {
			fatal(err)
		}
		outputs = append(outputs, outputFile{path: envelopeJSONPath, body: envelopeJSONBody, hint: "JSON Schema event envelope"})
	}

	if *check {
		for _, out := range outputs {
			existing, err := os.ReadFile(out.path) // #nosec G304 -- path from Taskfile / repo layout
			if err != nil || string(existing) != string(out.body) {
				fatal(fmt.Errorf("stale %s (%s) — run ./task codegen:wire-enums", out.path, out.hint))
			}
		}
		fmt.Fprintln(os.Stdout, "wire enum codegen OK")
		return
	}

	if *stageDir == "" {
		fatal(fmt.Errorf("--stage-dir is required to generate; scripts/codegen-wire-enums.sh publishes the staged tree"))
	}
	var manifest strings.Builder
	for _, out := range outputs {
		rel, relErr := filepath.Rel(absRoot, out.path)
		if relErr != nil || strings.HasPrefix(rel, "..") {
			fatal(fmt.Errorf("generated output outside repository: %s", out.path))
		}
		staged := filepath.Join(*stageDir, rel)
		if err := os.MkdirAll(filepath.Dir(staged), 0o750); err != nil {
			fatal(err)
		}
		if err := os.WriteFile(staged, out.body, 0o600); err != nil { // #nosec G306 -- generated artifact
			fatal(err)
		}
		manifest.WriteString(rel + "\n")
	}
	if err := os.WriteFile(filepath.Join(*stageDir, "manifest"), []byte(manifest.String()), 0o600); err != nil { // #nosec G306 -- staging metadata
		fatal(err)
	}
}

type outputFile struct {
	path string
	body []byte
	hint string
}

func findEnum(enums []EnumDef, name string) (EnumDef, bool) {
	for _, e := range enums {
		if e.Name == name {
			return e, true
		}
	}
	return EnumDef{}, false
}

func fatal(err error) {
	fmt.Fprintln(os.Stderr, "error:", err)
	os.Exit(1)
}

func kebabSchemaFile(name string) string {
	var b strings.Builder
	for i, r := range name {
		if i > 0 && r >= 'A' && r <= 'Z' {
			b.WriteByte('-')
		}
		if r >= 'A' && r <= 'Z' {
			b.WriteRune(r - 'A' + 'a')
		} else {
			b.WriteRune(r)
		}
	}
	return b.String()
}
