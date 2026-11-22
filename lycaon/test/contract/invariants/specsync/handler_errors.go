package specsync

import (
	"fmt"
	"net/http"
	"slices"
	"sort"
	"strconv"
	"strings"

	"github.com/lycaon/lycaon/internal/usernotice"
	wire "github.com/lycaon/lycaon/pkg/api"
	"github.com/lycaon/lycaon/test/contract/invariants"
)

// lycaonHandlers is the handler layer of the lycaon module.
var lycaonHandlers = handlerErrorTree{
	scope:     []string{lycaonModule + "/internal/", lycaonModule + "/pkg/"},
	httpLayer: lycaonModule + "/internal/api",
	register: map[string]bool{
		"(*" + lycaonModule + "/internal/api.Server).registerV1Operation": true,
		lycaonModule + "/internal/api.registerRootOperation":              true,
	},
	codeType:  lycaonModule + "/pkg/api.ApiErrorCode",
	errorBody: lycaonModule + "/pkg/api.ErrorResponse",
}

const (
	lycaonModule   = "github.com/lycaon/lycaon"
	userNoticesDir = "lycaon/config/packs/painted-wolf/platform/host/user-notices"
)

var handlerErrorRules = []invariants.Invariant{{
	Name:    "handlers-emit-only-declared-errors",
	Anchors: []invariants.Anchor{openAPI, routeDoor, invariants.Dir(userNoticesDir)},
	Check:   handlersEmitOnlyDeclaredErrors,
}}

// handlerScan is the SSA program of the module with its registered handlers,
// built once per corpus.
func handlerScan(r *invariants.Run, tree handlerErrorTree) (*handlerErrorProgram, []*handlerErrors) {
	type scan struct {
		prog     *handlerErrorProgram
		handlers []*handlerErrors
		problems []string
	}
	s := invariants.Part(r.Corpus, "handler errors", func() (scan, error) {
		prog := tree.load(r.Types(), r.Go().ModuleDir)
		handlers, problems := prog.scan()
		return scan{prog, handlers, problems}, nil
	})
	for _, problem := range s.problems {
		r.Report("registration %s", problem)
	}
	return s.prog, s.handlers
}

// Each code a handler can answer has a status its operation declares, and
// each details key it answers with is declared by the code's notice.
func handlersEmitOnlyDeclaredErrors(r *invariants.Run) {
	prog, handlers := handlerScan(r, lycaonHandlers)
	r.Expect(len(handlers), "registered handlers")
	operations := operationsByVariable(r)
	declared := map[string][]string{}
	for _, route := range specRoutes(r) {
		declared[route.OperationID] = route.DocumentedStatuses
	}
	unresolved := map[string][]string{}
	for _, h := range handlers {
		id := operations[h.operation].OperationID
		if id == "" {
			r.Report("%s: registered with no generated operation", h.operation)
			continue
		}
		for _, site := range h.unresolved {
			unresolved[site] = append(unresolved[site], id)
		}
		reportAll(r, undeclaredErrorViolations(id, h, declared[id], vocabularyStatus))
	}
	for site, ids := range unresolved {
		r.Report("%s: error code at %s does not resolve to a vocabulary constant (%d operations)", ids[0], site, len(ids))
	}
	cfg, err := usernotice.LoadNoticeDir(r.Path(userNoticesDir))
	must(r, "load the platform notice catalog", err)
	emitted := prog.detailsOf(handlers)
	reportAll(r, undeclaredDetailViolations(emitted, noticeContextSchema(cfg)))
	reportAll(r, unansweredContextViolations(emitted, cfg))
}

// unansweredContextViolations reports each render variable an HTTP-only notice
// declares that no handler answers its code with: handler details are that
// notice's only producer.
func unansweredContextViolations(emitted detailEmissions, cfg *usernotice.Config) []string {
	schema := noticeContextSchema(cfg)
	var out []string
	for code, entry := range cfg.UserNotices {
		if len(entry.Surfaces) != 1 || !entry.HasSurface("http") {
			continue
		}
		keys, _ := schema(code)
		for key := range keys {
			if !emitted.keys[code][key] {
				out = append(out, fmt.Sprintf("%s: context_schema key %s is never answered in details", code, key))
			}
		}
	}
	sort.Strings(out)
	return out
}

// noticeContextSchema answers the render variables a code's notice declares.
func noticeContextSchema(cfg *usernotice.Config) func(string) (map[string]bool, bool) {
	return func(code string) (map[string]bool, bool) {
		notice, ok := cfg.UserNotices[code]
		if !ok {
			return nil, false
		}
		out := map[string]bool{}
		for _, group := range []string{"required", "optional"} {
			list, _ := notice.ContextSchema[group].([]any)
			for _, item := range list {
				if name, ok := item.(string); ok {
					out[strings.TrimSpace(name)] = true
				}
			}
		}
		return out, true
	}
}

// vocabularyStatus is the one status the vocabulary declares for code, or 0
// when code is not in the vocabulary.
func vocabularyStatus(code string) int {
	if !slices.Contains(wire.AllApiErrorCodeValues(), wire.ApiErrorCode(code)) {
		return 0
	}
	return wire.ApiErrorCode(code).HTTPStatus()
}

// undeclaredErrorViolations reports each code whose status the operation does
// not declare. `default` covers only 500, the failure no request causes.
func undeclaredErrorViolations(id string, h *handlerErrors, statuses []string, status func(string) int) []string {
	declared := map[int]bool{}
	for _, key := range statuses {
		if key == "default" {
			declared[http.StatusInternalServerError] = true
			continue
		}
		if n, err := strconv.Atoi(key); err == nil {
			declared[n] = true
		}
	}
	var out []string
	for code := range h.codes {
		switch s := status(code); {
		case s == 0:
			out = append(out, fmt.Sprintf("%s: %s emits %s, which is not in the ApiErrorCode vocabulary", id, h.handler, code))
		case !declared[s]:
			out = append(out, fmt.Sprintf("%s: %s emits %s (%d) but declares %s", id, h.handler, code, s, strings.Join(sortedStatuses(statuses), " ")))
		}
	}
	return out
}

func sortedStatuses(statuses []string) []string {
	out := append([]string(nil), statuses...)
	sort.Strings(out)
	return out
}
