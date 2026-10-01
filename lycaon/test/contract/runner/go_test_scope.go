package contract

// Scope parsing for test tasks and workflows.

import (
	"regexp"
	"strings"
)

// goTestScope describes one Go test invocation.
type goTestScope struct {
	// Source identifies the task or workflow.
	Source string
	// Packages contains Go package patterns.
	Packages []string
	// Run is nil when the scope runs every test.
	Run *regexp.Regexp
	// RunSource is the raw filter.
	RunSource string
	// Command is the folded command line.
	Command string
}

// covers reports whether this scope runs the named test in the named package.
func (s goTestScope) covers(pkg, test string) bool {
	if !s.coversPackage(pkg) {
		return false
	}
	// The filter applies to the top-level test name.
	return s.Run == nil || s.Run.MatchString(test)
}

// coversPackage reports whether any of the scope's patterns select pkg.
func (s goTestScope) coversPackage(pkg string) bool {
	for _, pattern := range s.Packages {
		if recursive := strings.TrimSuffix(pattern, "/..."); recursive != pattern {
			if pkg == recursive || strings.HasPrefix(pkg, recursive+"/") {
				return true
			}
			continue
		}
		if pkg == pattern {
			return true
		}
	}
	return false
}

var goTestRunFilter = regexp.MustCompile(`-run\s+'([^']*)'`)

// parseGoTestScopes reads Go test invocations from shell text.
func parseGoTestScopes(source, text string) []goTestScope {
	var scopes []goTestScope
	for _, command := range yamlShellCommands(text) {
		if !strings.Contains(command, "go-test-digest.sh") && !strings.Contains(command, "test:digest") {
			continue
		}
		var packages []string
		for _, field := range strings.Fields(command) {
			if strings.HasPrefix(field, "./") {
				packages = append(packages, strings.TrimSuffix(field, "/"))
			}
		}
		if len(packages) == 0 {
			continue
		}
		scope := goTestScope{Source: source, Packages: packages, Command: command}
		if m := goTestRunFilter.FindStringSubmatch(command); m != nil {
			scope.RunSource = m[1]
			scope.Run = regexp.MustCompile(m[1])
		}
		scopes = append(scopes, scope)
	}
	return scopes
}

// yamlShellCommands folds YAML block commands.
func yamlShellCommands(text string) []string {
	var commands []string
	var current strings.Builder
	flush := func() {
		if strings.TrimSpace(current.String()) != "" {
			commands = append(commands, current.String())
		}
		current.Reset()
	}
	for _, line := range strings.Split(text, "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		if strings.HasPrefix(trimmed, "- ") || strings.HasPrefix(trimmed, "run:") {
			flush()
			trimmed = strings.TrimPrefix(trimmed, "- ")
			trimmed = strings.TrimSpace(strings.TrimPrefix(trimmed, "run:"))
			trimmed = strings.TrimSpace(strings.TrimPrefix(trimmed, ">"))
			trimmed = strings.TrimSpace(strings.TrimPrefix(trimmed, "|"))
		}
		current.WriteString(trimmed)
		current.WriteByte(' ')
	}
	flush()
	return commands
}

// goTestScopeNameToken matches names inside a filter.
var goTestScopeNameToken = regexp.MustCompile(`Test[A-Za-z0-9_]+`)

// deadRunNames finds filter names without test functions.
func deadRunNames(scope goTestScope, live map[string]struct{}) []string {
	if scope.RunSource == "" {
		return nil
	}
	var dead []string
	for _, name := range goTestScopeNameToken.FindAllString(scope.RunSource, -1) {
		if _, ok := live[name]; !ok {
			dead = append(dead, name)
		}
	}
	return dead
}
