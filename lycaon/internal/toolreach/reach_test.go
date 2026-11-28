package toolreach

import (
	"reflect"
	"testing"

	"github.com/lycaon/lycaon/internal/toolsurface"
)

func TestStatusFollowsTheFrozenRequest(t *testing.T) {
	plan := toolsurface.Compile([]string{"command", LoaderTool}, []string{"task"})
	withLoader := Freeze([]string{"command", LoaderTool}, plan)
	withoutLoader := Freeze([]string{"command"}, plan)
	cases := []struct {
		request Request
		tool    string
		want    Status
	}{
		{withLoader, "command", Callable},
		{withLoader, "task", Loadable},
		{withLoader, "absent", Absent},
		{withoutLoader, "task", Absent},
		{Request{}, "command", Absent},
	}
	for _, tc := range cases {
		if got := tc.request.Status(tc.tool); got != tc.want {
			t.Errorf("%s = %d, want %d", tc.tool, got, tc.want)
		}
	}
}

func TestSequenceLoadsADeferredTargetFirst(t *testing.T) {
	request := Freeze([]string{LoaderTool}, toolsurface.Compile([]string{LoaderTool}, []string{"task"}))
	call := Call{Tool: "task", Partial: true}
	got, ok := request.Sequence(t.Context(), call)
	if !ok || !reflect.DeepEqual(got, []Call{Load("task"), call}) {
		t.Fatalf("sequence = %#v, %v", got, ok)
	}
	if _, ok := request.Sequence(t.Context(), Call{Tool: "absent"}); ok {
		t.Fatal("an unreachable call produced a sequence")
	}
}

func TestCallTextShowsNestedArgumentsAndPartialCalls(t *testing.T) {
	call := Call{Tool: "command", Partial: true, Args: map[string]any{
		"capability_request": map[string]any{"read_path": "/supplied"},
		"background":         true,
	}}
	want := "`command(background=true, capability_request.read_path=\"/supplied\", …)`"
	if got := call.Text(); got != want {
		t.Fatalf("text = %s", got)
	}
}

func TestAcceptsFieldAndSequenceRequireAuthoritativeSchemasAndValidateEmptyMaps(t *testing.T) {
	plan := toolsurface.Compile([]string{LoaderTool}, []string{"command", "ask_user"})
	missingSchemaCallable := Offered([]string{"command", "ask_user"})
	if missingSchemaCallable.AcceptsField("command", "capability_request.process_control") {
		t.Fatal("missing schema map on callable tool must fail closed")
	}
	if missingSchemaCallable.AcceptsEnumValue("ask_user", "response_type", "secret") {
		t.Fatal("missing schema map on callable enum check must fail closed")
	}

	missingSchemaLoadable := Freeze([]string{LoaderTool}, plan)
	if missingSchemaLoadable.AcceptsField("command", "capability_request.process_control") {
		t.Fatal("loadable tool without projected schema must fail closed")
	}
	seq, ok := missingSchemaLoadable.Sequence(t.Context(), Call{
		Tool: "ask_user", Partial: true, Args: map[string]any{"response_type": "secret"},
	})
	if !ok || !reflect.DeepEqual(seq, []Call{Load("ask_user")}) {
		t.Fatalf("loadable tool without schema must retain loader only: %#v, %v", seq, ok)
	}

	narrowSchemas := map[string]map[string]any{
		"command": {
			"properties": map[string]any{
				"command": map[string]any{"type": "string"},
			},
		},
		"ask_user": {
			"properties": map[string]any{
				"response_type": map[string]any{"type": "string", "enum": []string{"text", "choice"}},
			},
		},
	}
	emptyObjCall := Call{
		Tool: "command", Partial: true,
		Args: map[string]any{"command": "ps", "capability_request": map[string]any{}},
	}
	if _, ok := Offered([]string{"command"}).WithSchemas(narrowSchemas).Sequence(t.Context(), emptyObjCall); ok {
		t.Fatal("callable tool with pruned capability_request admitted empty nested object")
	}
	if _, ok := Freeze([]string{LoaderTool}, plan).WithSchemas(narrowSchemas).Sequence(t.Context(), emptyObjCall); ok {
		t.Fatal("loadable tool with pruned capability_request admitted empty nested object")
	}
	if _, ok := Freeze([]string{LoaderTool}, plan).WithSchemas(narrowSchemas).Sequence(t.Context(), Call{
		Tool: "ask_user", Partial: true, Args: map[string]any{"response_type": "secret"},
	}); ok {
		t.Fatal("loadable tool with narrowed enum admitted secret response_type")
	}
}

func TestProgramInvocationSeparatesMultiModeAndGitSubcommands(t *testing.T) {
	genericGitAndTar := Offered([]string{"command"}).WithPrograms(func(name string) bool {
		return name == "git" || name == "tar" || name == "unzip" || name == "sed"
	})
	if genericGitAndTar.ProgramInvocation(t.Context(), "git", "config", "--list") {
		t.Fatal("generic git reach must not admit git config")
	}
	if !genericGitAndTar.ProgramInvocation(t.Context(), "tar", "-t", "-f", "a.tar") {
		t.Fatal("tar read reach must admit tar -t -f listing")
	}
	if genericGitAndTar.ProgramInvocation(t.Context(), "tar", "-x", "-f", "a.tar") {
		t.Fatal("generic tar reach without write mode admission must not admit tar -x -f extraction")
	}
	if !genericGitAndTar.ProgramInvocation(t.Context(), "unzip", "-p", "a.zip", "entry") {
		t.Fatal("unzip read reach must admit unzip -p stream inspection")
	}
	if !genericGitAndTar.ProgramInvocation(t.Context(), "sed", "p", "file.txt") {
		t.Fatal("sed read reach must admit sed p")
	}
	if genericGitAndTar.ProgramInvocation(t.Context(), "sed", "w out", "file.txt") {
		t.Fatal("sed read reach without w-command admission must reject sed 'w out'")
	}
	if genericGitAndTar.ProgramInvocation(t.Context(), "sed", "-i", ".bak", "s/a/b/", "file.txt") {
		t.Fatal("sed read reach must reject positional-suffix sed -i")
	}
	if genericGitAndTar.ProgramInvocation(t.Context(), "sed", "-f", "rules.sed", "file.txt") {
		t.Fatal("sed -f script-file reach must remain unknown")
	}
	if genericGitAndTar.ProgramMode("sed", "unknown-mode") || genericGitAndTar.ProgramMode("unknown-cmd", "always") {
		t.Fatal("unknown modes must fail closed")
	}

	fullModes := Offered([]string{"command"}).WithProgramTable(NewProgramTable(
		map[string]bool{"git": true, "git config": true, "tar": true, "sed": true},
		map[string]map[string]bool{
			"git config": {"query": true},
			"sed":        {"stream-script": true, "w-command": true, "-i": true},
			"tar":        {"listing-or-stream-decode": true, "create-extract-compress-decompress-to-path": true},
		},
	))
	if !fullModes.ProgramInvocation(t.Context(), "git", "config", "--get", "user.name") {
		t.Fatal("explicit git config query admission should allow git config --get")
	}
	if !fullModes.ProgramInvocation(t.Context(), "tar", "-x", "-f", "a.tar") {
		t.Fatal("explicit tar extraction mode admission should allow tar -x -f")
	}
	if !fullModes.ProgramInvocation(t.Context(), "sed", "p", "file.txt") || !fullModes.ProgramInvocation(t.Context(), "sed", "w out", "file.txt") {
		t.Fatal("writable sed mode admission should allow both sed p and sed 'w out'")
	}
	if !fullModes.ProgramInvocation(t.Context(), "sed", "-i", ".bak", "s/a/b/", "file.txt") {
		t.Fatal("in-place sed mode admission should allow positional-suffix sed -i .bak")
	}
	if fullModes.ProgramInvocation(t.Context(), "sed", "-i", ".bak", "s/a/b/") {
		t.Fatal("positional-suffix sed -i without file operand must fail closed")
	}
	if fullModes.ProgramInvocation(t.Context(), "sed", "-f", "rules.sed", "file.txt") {
		t.Fatal("sed -f without -i must remain unknown where file contents determine the mode")
	}
	if !fullModes.ProgramInvocation(t.Context(), "sed", "-i", "-f", "rules.sed", "file.txt") {
		t.Fatal("sed -i -f fixes in-place mode and should be admitted when -i is admitted")
	}
	if fullModes.ProgramMode("sed", "unknown-mode") {
		t.Fatal("unknown mode on full table must still fail closed")
	}
}
