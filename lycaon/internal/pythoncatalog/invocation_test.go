package pythoncatalog

import (
	"slices"
	"testing"
)

func TestValidateDirectToolArgs(t *testing.T) {
	t.Parallel()

	// Valid code mode with cwd and args preserved verbatim
	req, err := ValidateDirectToolArgs(map[string]any{
		"code": "import sys; print(sys.argv[1:])",
		"cwd":  "@repo/sub",
		"args": []any{"--flag", "$HOME", "a b"},
	})
	if err != nil {
		t.Fatalf("ValidateDirectToolArgs(code): %v", err)
	}
	if req.Mode != ModeCode || req.Cwd != "@repo/sub" || !slices.Equal(req.Args, []string{"--flag", "$HOME", "a b"}) {
		t.Fatalf("unexpected code req: %+v", req)
	}

	// Valid script mode with []string args
	req, err = ValidateDirectToolArgs(map[string]any{
		"script": "scripts/build.py",
		"args":   []string{"-v", "--out=a.xlsx"},
	})
	if err != nil || req.Mode != ModeScript || req.Script != "scripts/build.py" {
		t.Fatalf("ValidateDirectToolArgs(script) = (%+v, %v)", req, err)
	}

	// Valid module mode
	req, err = ValidateDirectToolArgs(map[string]any{
		"module": "pytest",
		"args":   []any{"-q", "tests/test_app.py"},
	})
	if err != nil || req.Mode != ModeModule || req.Module != "pytest" {
		t.Fatalf("ValidateDirectToolArgs(module) = (%+v, %v)", req, err)
	}

	// Invalid cases
	invalid := []map[string]any{
		nil,
		{},
		{"code": "print(1)", "script": "a.py"},
		{"script": "a.py", "module": "pytest"},
		{"code": "   "},
		{"script": ""},
		{"module": 123},
		{"code": "print(1)", "cwd": 42},
		{"code": "print(1)", "args": "not-a-slice"},
		{"code": "print(1)", "args": []any{"ok", 7}},
		{"code": "print(1)", "unknown": true},
	}
	for i, args := range invalid {
		if _, err := ValidateDirectToolArgs(args); err == nil {
			t.Errorf("case %d (%v): expected error, got nil", i, args)
		}
	}
}

func TestParsePythonCommandLineOperandBoundary(t *testing.T) {
	t.Parallel()

	// -c ends interpreter option parsing and forwards remaining args verbatim
	parsed, err := ParsePythonCommandLine([]string{"python3", "-u", "-W", "ignore", "-c", "import sys; print(sys.argv)", "-v", "--help", "-c", "not-an-option"})
	if err != nil {
		t.Fatalf("ParsePythonCommandLine(-c): %v", err)
	}
	if parsed.Executable != "python3" || parsed.Mode != ModeCode || parsed.Target != "import sys; print(sys.argv)" {
		t.Fatalf("unexpected -c parse: %+v", parsed)
	}
	if !slices.Equal(parsed.Flags, []string{"-u", "-W", "ignore"}) {
		t.Fatalf("flags = %v", parsed.Flags)
	}
	if !slices.Equal(parsed.Argv, []string{"-v", "--help", "-c", "not-an-option"}) {
		t.Fatalf("argv = %v", parsed.Argv)
	}

	// -m ends interpreter option parsing and forwards flags to the module
	parsed, err = ParsePythonCommandLine([]string{"python", "-B", "-m", "pytest", "-q", "-k", "test_one", "--tb=short"})
	if err != nil {
		t.Fatalf("ParsePythonCommandLine(-m): %v", err)
	}
	if parsed.Mode != ModeModule || parsed.Target != "pytest" || !slices.Equal(parsed.Argv, []string{"-q", "-k", "test_one", "--tb=short"}) {
		t.Fatalf("unexpected -m parse: %+v", parsed)
	}

	// Attached -c<code> and -m<module> forms
	parsed, err = ParsePythonCommandLine([]string{"python3", "-cprint(1)", "arg1"})
	if err != nil || parsed.Mode != ModeCode || parsed.Target != "print(1)" || !slices.Equal(parsed.Argv, []string{"arg1"}) {
		t.Fatalf("attached -c parse = (%+v, %v)", parsed, err)
	}
	parsed, err = ParsePythonCommandLine([]string{"python3", "-mjson.tool", "--indent", "2"})
	if err != nil || parsed.Mode != ModeModule || parsed.Target != "json.tool" || !slices.Equal(parsed.Argv, []string{"--indent", "2"}) {
		t.Fatalf("attached -m parse = (%+v, %v)", parsed, err)
	}

	// Script path ends interpreter option parsing
	parsed, err = ParsePythonCommandLine([]string{"python3", "scripts/run.py", "-m", "not-module", "--flag", "val"})
	if err != nil || parsed.Mode != ModeScript || parsed.Target != "scripts/run.py" || !slices.Equal(parsed.Argv, []string{"-m", "not-module", "--flag", "val"}) {
		t.Fatalf("script parse = (%+v, %v)", parsed, err)
	}

	// Explicit stdin marker `-` ends option parsing
	parsed, err = ParsePythonCommandLine([]string{"python3", "-", "--script-arg", "1"})
	if err != nil || parsed.Mode != ModeStdin || parsed.Target != "-" || !slices.Equal(parsed.Argv, []string{"--script-arg", "1"}) {
		t.Fatalf("stdin parse = (%+v, %v)", parsed, err)
	}

	// Implicit stdin (no operand)
	parsed, err = ParsePythonCommandLine([]string{"python3", "-u"})
	if err != nil || parsed.Mode != ModeStdin || parsed.Target != "-" {
		t.Fatalf("implicit stdin parse = (%+v, %v)", parsed, err)
	}

	// Version and help
	parsed, err = ParsePythonCommandLine([]string{"python3", "--version"})
	if err != nil || parsed.Mode != ModeVersion {
		t.Fatalf("version parse = (%+v, %v)", parsed, err)
	}
	parsed, err = ParsePythonCommandLine([]string{"python", "--help"})
	if err != nil || parsed.Mode != ModeHelp {
		t.Fatalf("help parse = (%+v, %v)", parsed, err)
	}

	// Errors
	for _, bad := range [][]string{
		{},
		{"ruby", "-e", "puts 1"},
		{"python3", "-c"},
		{"python3", "-m"},
		{"python3", "-W"},
		{"python3", "--not-a-version-1-option"},
	} {
		if _, err := ParsePythonCommandLine(bad); err == nil {
			t.Errorf("ParsePythonCommandLine(%v): expected error", bad)
		}
	}
}

func TestCheckBareInterpreterRouting(t *testing.T) {
	t.Parallel()

	// Blocked when direct python tool is offered and bare python/python3 is used
	for _, argv := range [][]string{
		{"python", "-c", "print(1)"},
		{"python3", "script.py"},
		{"python3", "-m", "pytest"},
	} {
		dec := CheckBareInterpreterRouting(argv, true)
		if !dec.Blocked || dec.Code != RoutingCodePythonToolRequired || dec.Command != argv[0] {
			t.Errorf("CheckBareInterpreterRouting(%v, true) = %+v, want blocked PYTHON_TOOL_REQUIRED", argv, dec)
		}
	}

	// Allowed when direct python tool is NOT offered, or explicit external -- python3, or path-qualified interpreter
	allowed := []struct {
		argv    []string
		offered bool
	}{
		{[]string{"python3", "-c", "print(1)"}, false},
		{[]string{"external", "--", "python3", "-c", "print(1)"}, true},
		{[]string{"/usr/bin/python3", "script.py"}, true},
		{[]string{".venv/bin/python", "-m", "pytest"}, true},
		{[]string{"pytest", "-q"}, true},
		{nil, true},
	}
	for _, tc := range allowed {
		dec := CheckBareInterpreterRouting(tc.argv, tc.offered)
		if dec.Blocked {
			t.Errorf("CheckBareInterpreterRouting(%v, %v) unexpectedly blocked: %+v", tc.argv, tc.offered, dec)
		}
	}
}
