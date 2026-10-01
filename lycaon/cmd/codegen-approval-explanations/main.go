// Command codegen-approval-explanations updates each pack's approval entries.
package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"

	"github.com/lycaon/lycaon/internal/approvalregistry"
	"github.com/lycaon/lycaon/internal/approvals"
)

func main() {
	check := flag.Bool("check", false, "exit 1 if generated outputs are stale")
	flag.Parse()

	stock, err := approvals.LoadConfigStock()
	if err != nil {
		fatal(err)
	}
	entries, err := approvalregistry.ListEffective()
	if err != nil {
		fatal(err)
	}
	pathByKey := map[string]string{}
	for _, ent := range entries {
		pathByKey[ent.Key] = onDisk(ent.Path.String())
	}

	expected := &approvals.Config{Explanations: map[string]approvals.ExplanationEntry{}}
	for _, key := range approvals.GateableKeys() {
		entry, ok := stock.Explanations[key]
		if !ok {
			entry = defaultEntry(key)
		}
		expected.Explanations[key] = enrichEntry(key, entry)
	}

	if err := approvals.ValidateConfig(expected); err != nil {
		fatal(err)
	}

	if *check {
		for _, key := range approvals.GateableKeys() {
			got, ok := stock.Explanations[key]
			if !ok {
				fatal(fmt.Errorf("missing approval explanation %q — run ./task codegen:approval-explanations", key))
			}
			if !reflect.DeepEqual(got, expected.Explanations[key]) {
				fatal(fmt.Errorf("stale approval explanation %q — run ./task codegen:approval-explanations", key))
			}
		}
		return
	}

	platformDir := onDisk(approvalregistry.DefaultDir.String())
	for _, key := range approvals.GateableKeys() {
		path, ok := pathByKey[key]
		if !ok {
			path = filepath.Join(platformDir, key, approvalregistry.ExplainManifestName)
		}
		if err := approvals.WriteExplainYAML(path, key, expected.Explanations[key]); err != nil {
			fatal(err)
		}
	}
	fmt.Println("wrote stock approval explanations")
}

// onDisk maps a bundled source path, which is relative to the embedded config
// filesystem, to its checkout location. Writes run from the Go module dir.
func onDisk(rel string) string {
	if filepath.IsAbs(rel) {
		return rel
	}
	return filepath.Join("config", filepath.FromSlash(rel))
}

func defaultEntry(key string) approvals.ExplanationEntry {
	spec, ok := entrySpecs[key]
	if !ok {
		spec = entrySpecs[approvals.FallbackExplanationKey]
	}
	return approvals.ExplanationEntry{
		Tool:        key,
		TierHint:    spec.tier,
		WhatChanges: spec.what,
		WhoAffected: spec.who,
		IfWrong:     spec.ifWrong,
		AllowLine:   spec.allow,
		Scenarios: []approvals.ScenarioEntry{
			{
				ID:             "default",
				Vars:           spec.vars,
				ExpectContains: append([]string(nil), spec.expect...),
			},
		},
	}
}

func enrichEntry(key string, entry approvals.ExplanationEntry) approvals.ExplanationEntry {
	spec, ok := entrySpecs[key]
	if !ok {
		spec = entrySpecs[approvals.FallbackExplanationKey]
	}
	if strings.TrimSpace(entry.Tool) == "" {
		entry.Tool = key
	}
	if strings.TrimSpace(entry.TierHint) == "" {
		entry.TierHint = spec.tier
	}
	if strings.TrimSpace(entry.WhatChanges) == "" {
		entry.WhatChanges = spec.what
	}
	if strings.TrimSpace(entry.WhoAffected) == "" {
		entry.WhoAffected = spec.who
	}
	if strings.TrimSpace(entry.IfWrong) == "" {
		entry.IfWrong = spec.ifWrong
	}
	if strings.TrimSpace(entry.AllowLine) == "" {
		entry.AllowLine = spec.allow
	}
	if len(entry.Scenarios) == 0 {
		entry.Scenarios = []approvals.ScenarioEntry{{
			ID:             "default",
			Vars:           spec.vars,
			ExpectContains: append([]string(nil), spec.expect...),
		}}
	}
	return entry
}

type spec struct {
	tier    string
	what    string
	who     string
	ifWrong string
	allow   string
	vars    map[string]any
	expect  []string
}

var entrySpecs = map[string]spec{
	"process_signal": {
		tier:    "recoverable",
		what:    "Signal the selected host process instances.",
		who:     "The selected processes and any work they have not finished.",
		ifWrong: "Signals can interrupt work or lose unsaved data. The host rechecks each process instance before signaling.",
		allow:   "signaling the selected processes",
		vars:    map[string]any{"project": "my-app"},
		expect:  []string{"process instances", "unsaved data"},
	},
	approvals.KeyCommand: {
		tier:    "recoverable",
		what:    "Run shell command: {{ command }}.",
		who:     "This project — output and side effects stay on your machine until shared.",
		ifWrong: "Usually reversible locally; review the exact command before allowing.",
		allow:   "running `{{ command }}`",
		vars:    map[string]any{"command": "npm run build", "project": "my-app"},
		expect:  []string{"shell command", "npm run build"},
	},
	approvals.KeyCommandDestructive: {
		tier:    "irreversible",
		what:    "Runs a permanent or high-impact shell command that can cross the sandbox boundary (beyond attached folders, temp, and allowed cache locations).",
		who:     "Systems outside allowed write roots — remotes, shared services, or data you cannot trivially restore.",
		ifWrong: "Often cannot be undone from here once it completes.",
		allow:   "running `{{ command }}`",
		vars:    map[string]any{"command": "git push origin main", "project": "my-app"},
		expect:  []string{"permanent", "sandbox boundary"},
	},
	"command_stop": {
		tier:    "recoverable",
		what:    "{% if command %}Stop the background command: {{ command }}.{% else %}Stop the selected background command.{% endif %}",
		who:     "The running command and any work it has not finished.",
		ifWrong: "Stopping it interrupts the process tree; partial work may need review.",
		allow:   "{% if command %}stopping `{{ command }}`{% else %}stopping the selected background command{% endif %}",
		vars:    map[string]any{"command": "npm run dev", "project": "my-app"},
		expect:  []string{"Stop the background command", "npm run dev"},
	},
	approvals.KeyMCPCall: {
		tier:    "recoverable",
		what:    "Call external action {{ provider }}.{{ tool }}.",
		who:     "The connected service and anything it can reach on your behalf.",
		ifWrong: "External effects may persist even if local files are unchanged.",
		allow:   "the `{{ tool }}` action",
		vars:    map[string]any{"provider": "docs", "tool": "search_docs"},
		expect:  []string{"external action", "search_docs"},
	},
	approvals.KeyPathOutsideProject: {
		tier:    "irreversible",
		what:    "Change files outside attached folders at {{ path }}.",
		who:     "Paths outside the attached folder boundary.",
		ifWrong: "Not confined to project undo — may affect other repos or system files.",
		allow:   "changing files outside attached folders",
		vars:    map[string]any{"path": "/etc/hosts", "project": "my-app"},
		expect:  []string{"outside attached folders", "/etc/hosts"},
	},
	approvals.FallbackExplanationKey: {
		tier:    "irreversible",
		what:    "Run {{ tool }} with the proposed arguments.",
		who:     "This project or connected systems the action can reach.",
		ifWrong: "May be difficult to undo — review before allowing.",
		allow:   "this action",
		vars:    map[string]any{"tool": "custom_tool", "project": "my-app"},
		expect:  []string{"proposed arguments", "difficult to undo"},
	},
	"git_commit": {
		tier:    "recoverable",
		what:    "Record a commit in this repository's local history.",
		who:     "Your project — the commit becomes part of local git history.",
		ifWrong: "Reversible locally with reset or amend; not shared until you push.",
		allow:   "recording commits in this repository",
		vars:    map[string]any{"project": "my-app"},
		expect:  []string{"commit", "local history"},
	},
	"git_restore": {
		tier:    "recoverable",
		what:    "Restore working-tree files from git history.",
		who:     "Uncommitted or tracked files in this project.",
		ifWrong: "Local changes can be lost — recoverable from reflog in many cases.",
		allow:   "restoring files from git",
		vars:    map[string]any{"path": "src/app.ts", "project": "my-app"},
		expect:  []string{"Restore", "git history"},
	},
	"chmod": {
		tier:    "recoverable",
		what:    "Change file permissions on {{ path }}.",
		who:     "The named path in this project.",
		ifWrong: "Permissions can break access until changed back.",
		allow:   "changing permissions on project files",
		vars:    map[string]any{"path": "scripts/run.sh", "project": "my-app"},
		expect:  []string{"permissions", "scripts/run.sh"},
	},
	"copy": {
		tier:    "recoverable",
		what:    "Copy files within the project.",
		who:     "Source and destination paths in this project.",
		ifWrong: "Overwrites are usually reversible via git if tracked.",
		allow:   "copying files in this project",
		vars:    map[string]any{"path": "src/a.ts", "project": "my-app"},
		expect:  []string{"Copy files", "project"},
	},
	"move": {
		tier:    "recoverable",
		what:    "Move or rename paths in the project.",
		who:     "The affected files and directories in this project.",
		ifWrong: "Usually reversible via git when paths are tracked.",
		allow:   "moving files in this project",
		vars:    map[string]any{"path": "src/old.ts", "project": "my-app"},
		expect:  []string{"Move", "project"},
	},
	"mkdir": {
		tier:    "recoverable",
		what:    "Create directories in the project.",
		who:     "The project working tree.",
		ifWrong: "Empty dirs are easy to remove; tracked content stays in git history.",
		allow:   "creating directories in this project",
		vars:    map[string]any{"path": "src/new", "project": "my-app"},
		expect:  []string{"Create directories", "project"},
	},
	"delete": {
		tier:    "recoverable",
		what:    "Delete {{ path }} from the working tree.",
		who:     "The named path in this project.",
		ifWrong: "Recoverable from git when the file was tracked.",
		allow:   "deleting files in this project",
		vars:    map[string]any{"path": "tmp/scratch.txt", "project": "my-app"},
		expect:  []string{"Delete", "working tree"},
	},
	"extract_archive": {
		tier:    "recoverable",
		what:    "Extract an archive into the project.",
		who:     "Destination paths in this project — existing files may be overwritten.",
		ifWrong: "Overwrites may be reversible via git for tracked paths.",
		allow:   "extracting archives in this project",
		vars:    map[string]any{"path": "vendor/pkg", "project": "my-app"},
		expect:  []string{"Extract", "project"},
	},
	"web_search": {
		tier:    "recoverable",
		what:    "Search the public web for information.",
		who:     "External search providers — read-only network access.",
		ifWrong: "No local mutation; queries leave your network.",
		allow:   "web search requests",
		vars:    map[string]any{"project": "my-app"},
		expect:  []string{"Search", "web"},
	},
	"fetch_url": {
		tier:    "recoverable",
		what:    "Fetch content from a URL.",
		who:     "The remote host — read-only network access.",
		ifWrong: "No local mutation; the request leaves your network.",
		allow:   "fetching URLs",
		vars:    map[string]any{"project": "my-app"},
		expect:  []string{"Fetch", "URL"},
	},
	"task": {
		tier:    "recoverable",
		what:    "Dispatch a worker to perform assigned work.",
		who:     "This session — a write-mode worker may mutate files allowed by its tool profile and confinement.",
		ifWrong: "Worker changes land in overlays until you promote or discard.",
		allow:   "dispatching workers in this session",
		vars:    map[string]any{"project": "my-app"},
		expect:  []string{"worker", "assigned work"},
	},
	"render_view": {
		tier:    "recoverable",
		what:    "Rasterize authored HTML or SVG into a visual mockup.",
		who:     "This session — a sandboxed headless browser renders markup to an image artifact.",
		ifWrong: "No project files change; the mockup is session-scoped and can be discarded.",
		allow:   "rendering visual mockups",
		vars:    map[string]any{"project": "my-app"},
		expect:  []string{"Rasterize", "mockup"},
	},
	"capture_page": {
		tier:    "recoverable",
		what:    "Capture a screenshot and structural snapshot of a page.",
		who:     "This session — a headless browser visits the target URL or served path.",
		ifWrong: "Network/local page access leaves your machine; no project files are mutated.",
		allow:   "capturing page screenshots",
		vars:    map[string]any{"project": "my-app"},
		expect:  []string{"Capture", "screenshot"},
	},
	"measure_page": {
		tier:    "recoverable",
		what:    "Measure render-tree geometry for a page.",
		who:     "This session — a headless browser collects layout numbers from the target page.",
		ifWrong: "Read-only measurement; no project files are mutated.",
		allow:   "measuring page geometry",
		vars:    map[string]any{"project": "my-app"},
		expect:  []string{"Measure", "geometry"},
	},
	"chown": {
		tier:    "irreversible",
		what:    "Change ownership on {{ path }}.",
		who:     "The named path — ownership affects who can access the file.",
		ifWrong: "Ownership changes can lock you out until reversed with sufficient privileges.",
		allow:   "changing file ownership",
		vars:    map[string]any{"path": "data/db", "project": "my-app"},
		expect:  []string{"ownership", "path"},
	},
	"workflow_persist": {
		tier:    "irreversible",
		what:    "Persist workflow state to disk.",
		who:     "Project workflow configuration and run records.",
		ifWrong: "Saved workflow artifacts affect future runs.",
		allow:   "persisting workflow state",
		vars:    map[string]any{"project": "my-app"},
		expect:  []string{"Persist workflow", "disk"},
	},
	"workflow_compose": {
		tier:    "irreversible",
		what:    "Compose a new workflow definition.",
		who:     "Project workflow catalog and future session behavior.",
		ifWrong: "New workflows change what the agent can start later.",
		allow:   "composing workflows",
		vars:    map[string]any{"project": "my-app"},
		expect:  []string{"Compose", "workflow"},
	},
	"workflow_compose_from_template": {
		tier:    "irreversible",
		what:    "Create a workflow from a template.",
		who:     "Project workflow catalog and future session behavior.",
		ifWrong: "Adds a durable workflow the agent can invoke again.",
		allow:   "creating workflows from templates",
		vars:    map[string]any{"project": "my-app"},
		expect:  []string{"workflow", "template"},
	},
	"workflow_advance": {
		tier:    "irreversible",
		what:    "Advance the active workflow phase.",
		who:     "The running workflow — gates and downstream phases may unlock.",
		ifWrong: "Phase transitions can be hard to walk back once committed.",
		allow:   "advancing workflow phases",
		vars:    map[string]any{"project": "my-app"},
		expect:  []string{"Advance", "workflow phase"},
	},
	"workflow_user_feedback": {
		tier:    "irreversible",
		what:    "Record user feedback on the active workflow.",
		who:     "The workflow run — feedback may influence later phases.",
		ifWrong: "Recorded feedback persists on the run.",
		allow:   "recording workflow feedback",
		vars:    map[string]any{"project": "my-app"},
		expect:  []string{"feedback", "workflow"},
	},
}

func fatal(err error) {
	fmt.Fprintln(os.Stderr, err)
	os.Exit(1)
}
