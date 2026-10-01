package api

import (
	"regexp"
	"strings"
)

// credentialRequestOperations declare bodies that require full redaction.
var credentialRequestOperations = []generatedOperation{
	operationCreateComposerSecret,
	operationCreateProjectManagedSecret,
	operationReplaceProjectManagedSecretValue,
	operationResolveWorkflowSecret,
	operationReplaceProviderCredential,
	operationReplaceWebResearchCredential,
	operationCreateMcpProvider,
	operationUpdateMcpProvider,
	operationCompleteMcpOAuth,
}

var withheldCredentialResponseOperations = []generatedOperation{
	operationCompleteProjectManagedSecretReveal,
}

var credentialRequestMatchers = buildCredentialMatchers(credentialRequestOperations)
var credentialResponseMatchers = buildCredentialMatchers(withheldCredentialResponseOperations)

type credentialOperationMatcher struct {
	method string
	path   *regexp.Regexp
}

func buildCredentialMatchers(operations []generatedOperation) []credentialOperationMatcher {
	out := make([]credentialOperationMatcher, 0, len(operations))
	for _, op := range operations {
		out = append(out, credentialOperationMatcher{
			method: strings.ToUpper(op.Method), path: compilePathTemplate(op.Path),
		})
	}
	return out
}

var pathParameterRE = regexp.MustCompile(`\{[^/}]+\}`)

func compilePathTemplate(template string) *regexp.Regexp {
	var parts []string
	for _, segment := range strings.Split(strings.Trim(template, "/"), "/") {
		if pathParameterRE.MatchString(segment) {
			parts = append(parts, `[^/]+`)
			continue
		}
		parts = append(parts, regexp.QuoteMeta(segment))
	}
	return regexp.MustCompile(`^/` + strings.Join(parts, "/") + `/?$`)
}

func requestBodyCarriesCredential(method, path string) bool {
	return matchesCredentialOperation(credentialRequestMatchers, method, path)
}

func responseBodyMustBeWithheld(method, path string) bool {
	return matchesCredentialOperation(credentialResponseMatchers, method, path)
}

func matchesCredentialOperation(matchers []credentialOperationMatcher, method, path string) bool {
	method = strings.ToUpper(strings.TrimSpace(method))
	for _, matcher := range matchers {
		if matcher.method == method && matcher.path.MatchString(path) {
			return true
		}
	}
	return false
}
