package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/odvcencio/gotreesitter/grammargen"
)

const minimumTreeSitterCLIMinor = 26

func importGrammarJSWithCLI(path, executable string) (*grammargen.Grammar, error) {
	cli, err := exec.LookPath(executable)
	if err != nil {
		return nil, fmt.Errorf("find tree-sitter CLI: %w", err)
	}
	if err := requireTreeSitterCLIVersion(cli); err != nil {
		return nil, err
	}
	if err := requireTreeSitterCLICapabilities(cli); err != nil {
		return nil, err
	}

	grammarPath, err := filepath.Abs(path)
	if err != nil {
		return nil, fmt.Errorf("resolve grammar.js path: %w", err)
	}
	if info, err := os.Stat(grammarPath); err != nil {
		return nil, fmt.Errorf("read grammar.js: %w", err)
	} else if info.IsDir() {
		return nil, fmt.Errorf("read grammar.js: %s is a directory", grammarPath)
	}

	outputDir, err := os.MkdirTemp("", "gotreesitter-grammar-js-")
	if err != nil {
		return nil, fmt.Errorf("create grammar.json output directory: %w", err)
	}
	defer os.RemoveAll(outputDir)

	cmd := exec.Command(cli,
		"generate",
		"--no-parser",
		"--output", outputDir,
		filepath.Base(grammarPath),
	)
	cmd.Dir = filepath.Dir(grammarPath)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return nil, grammarJSGenerateError(err, output)
	}

	jsonPath := filepath.Join(outputDir, "grammar.json")
	data, err := os.ReadFile(jsonPath)
	if err != nil {
		return nil, fmt.Errorf("read generated grammar.json: %w", err)
	}
	g, err := grammargen.ImportGrammarJSON(data)
	if err != nil {
		return nil, fmt.Errorf("import generated grammar.json: %w", err)
	}
	return g, nil
}

func grammarJSGenerateError(err error, output []byte) error {
	cause := commandError("tree-sitter generate", err, output)
	if strings.Contains(string(output), "Failed to run `node`") {
		return fmt.Errorf(
			"-js-cli requires Tree-sitter CLI 0.26 or newer and a JavaScript runtime such as Node on PATH: %w",
			cause,
		)
	}
	return cause
}

func requireTreeSitterCLICapabilities(cli string) error {
	output, err := exec.Command(cli, "generate", "--help").CombinedOutput()
	if err != nil {
		return commandError("tree-sitter generate --help", err, output)
	}
	var missing []string
	for _, flag := range []string{"--no-parser", "--output"} {
		if !strings.Contains(string(output), flag) {
			missing = append(missing, flag)
		}
	}
	if len(missing) != 0 {
		return fmt.Errorf(
			"tree-sitter CLI lacks required generate flags for -js-cli: %s",
			strings.Join(missing, ", "),
		)
	}
	return nil
}

func requireTreeSitterCLIVersion(cli string) error {
	output, err := exec.Command(cli, "--version").CombinedOutput()
	if err != nil {
		return commandError("tree-sitter --version", err, output)
	}
	major, minor, err := parseTreeSitterCLIVersion(string(output))
	if err != nil {
		return err
	}
	if major == 0 && minor < minimumTreeSitterCLIMinor {
		return fmt.Errorf(
			"tree-sitter CLI 0.%d or newer is required for -js-cli; found %d.%d",
			minimumTreeSitterCLIMinor,
			major,
			minor,
		)
	}
	return nil
}

func parseTreeSitterCLIVersion(output string) (int, int, error) {
	for _, field := range strings.Fields(output) {
		version := strings.TrimPrefix(field, "v")
		parts := strings.Split(version, ".")
		if len(parts) < 2 {
			continue
		}
		major, majorErr := strconv.Atoi(parts[0])
		minor, minorErr := strconv.Atoi(parts[1])
		if majorErr == nil && minorErr == nil {
			return major, minor, nil
		}
	}
	return 0, 0, fmt.Errorf("parse tree-sitter CLI version from %q", strings.TrimSpace(output))
}

func commandError(operation string, err error, output []byte) error {
	message := strings.TrimSpace(string(output))
	if message == "" {
		return fmt.Errorf("%s: %w", operation, err)
	}
	return fmt.Errorf("%s: %w: %s", operation, err, message)
}
