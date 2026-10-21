package contract

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/coordinator/surfacecatalog"
	"github.com/lycaon/lycaon/internal/hintregistry"
	"github.com/lycaon/lycaon/internal/toolcontract"
	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
	"gopkg.in/yaml.v3"
)

// retiredRoutineSchemas are the structured file and search schemas command
// replaces on the routine floor. They stay loadable by exact name.
var retiredRoutineSchemas = []string{"read", "grep", "find", "list_dir", "jq"}

// retiredRedirectCodes told a model to leave command for a structured schema.
// Their meanings are retired, never reused.
var retiredRedirectCodes = []string{
	"USE_CHMOD_NATIVE", "USE_CHOWN_NATIVE", "USE_COPY_NATIVE", "USE_DELETE_NATIVE", "USE_FIND_NOT_COMMAND",
	"USE_GIT_DIFF_NATIVE", "USE_GIT_STATUS_NATIVE", "USE_GREP_REGEX", "USE_HTTP_REQUEST_NATIVE", "USE_JQ_NATIVE",
	"USE_LIST_DIR_NATIVE", "USE_MKDIR_NATIVE", "USE_MOVE_NATIVE", "USE_NATIVE_TOOL", "USE_READ_NATIVE",
	"USE_STAT_NATIVE", "USE_WC_NATIVE", "USE_WRITE_NATIVE",
}

func TestRoutineFloorsOfferCommandInPlaceOfRetiredSchemas(t *testing.T) {
	t.Parallel()
	catalog, err := surfacecatalog.Load()
	contractcheck.FailErr(t, "load coordinator surfaces", err)
	for _, id := range catalog.SurfaceIDs() {
		surface, err := catalog.Surface(id)
		contractcheck.FailErr(t, "read surface "+id, err)
		floor := surface.FloorTools()
		for _, retired := range retiredRoutineSchemas {
			if slices.Contains(floor, retired) {
				t.Errorf("%s still offers retired routine schema %s on its floor", id, retired)
			}
		}
		if slices.Contains(floor, "command") && slices.Contains(surface.LoadableTools(), "command") {
			t.Errorf("%s lists command on both its floor and its loadable set", id)
		}
	}
	for _, id := range []string{"implement_investigate", "observe_investigate", "plan_review", "await_host"} {
		surface, err := catalog.Surface(id)
		if err != nil || !slices.Contains(surface.FloorTools(), "command") {
			t.Errorf("%s does not offer command on its floor", id)
		}
	}
}

func TestToolProfilesDeferRetiredSchemasAndOfferReadOnlyCommand(t *testing.T) {
	t.Parallel()
	for _, id := range []string{"coordinator", "implement", "worker_readonly", "explore_readonly", "plan_review_readonly"} {
		profile := loadToolProfileByID(t, id)
		for _, retired := range retiredRoutineSchemas {
			if profile.StickyTools[retired] {
				t.Errorf("%s loads retired routine schema %s up front", id, retired)
			}
		}
		if !profile.ToolAllowed("command") || !profile.StickyTools["command"] {
			t.Errorf("%s does not offer command up front", id)
		}
	}
	for _, id := range []string{"worker_readonly", "explore_readonly", "plan_review_readonly"} {
		if classes := loadToolProfileByID(t, id).CommandEffects.Classes(); !slices.Equal(classes, []toolcontract.CommandEffectClass{toolcontract.CommandReadOnly}) {
			t.Errorf("%s command is not read-only", id)
		}
	}
}

func TestRetiredCommandRedirectsAreGone(t *testing.T) {
	t.Parallel()
	entries, err := hintregistry.ListEffective()
	contractcheck.FailErr(t, "list effective policy", err)
	for _, entry := range entries {
		if slices.Contains(retiredRedirectCodes, entry.Code) {
			t.Errorf("retired command redirect %s is still a policy unit", entry.Code)
		}
	}
	root := contractcheck.RepoRoot(t)
	schemas, err := filepath.Glob(filepath.Join(root, "lycaon", "config", "packs", "*", "*", "tools", "schemas", "*.yaml"))
	contractcheck.FailErr(t, "list tool schemas", err)
	for _, path := range schemas {
		data, err := os.ReadFile(path)
		contractcheck.FailErr(t, "read schema", err)
		var schema struct {
			Description string `yaml:"description"`
		}
		contractcheck.FailErr(t, "parse "+path, yaml.Unmarshal(data, &schema))
		for _, phrase := range []string{"Replaces command:", "never use command"} {
			if strings.Contains(schema.Description, phrase) {
				t.Errorf("%s still steers away from command: %q", filepath.Base(path), phrase)
			}
		}
	}
	for _, unit := range []string{"native-read-tool", "native-grep-tool", "native-find-tool", "native-list-dir-tool", "native-jq-tool"} {
		if _, err := os.Stat(filepath.Join(root, "lycaon", "config", "packs", "painted-wolf", "platform", "shared", "units", unit+".md")); !os.IsNotExist(err) {
			t.Errorf("retired routine tutorial %s remains", unit)
		}
	}
}
