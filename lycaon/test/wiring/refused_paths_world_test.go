package wiring

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/configdir"
	"github.com/lycaon/lycaon/internal/gitengine"
	"github.com/lycaon/lycaon/internal/llm"
	"github.com/lycaon/lycaon/internal/nativemanifest"
	"github.com/lycaon/lycaon/internal/settingsoverlay"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/testutil/gittest"
	wire "github.com/lycaon/lycaon/pkg/api"
)

// outsideCanary is the outside file's content; no refused call may deliver it.
const outsideCanary = `{"canary":"outside 5c1d7a"}`

// pathTemplate is one path-taking tool or operation; {f} is replaced by a
// spelling of the outside file and {d} by a spelling of the outside directory.
type pathTemplate struct {
	covers string
	tool   string
	args   map[string]any
}

func (p pathTemplate) usesDir() bool {
	raw, _ := json.Marshal(p.args)
	return strings.Contains(string(raw), "{d}")
}

func (p pathTemplate) bind(file, dir string) map[string]any {
	raw, _ := json.Marshal(p.args)
	text := strings.NewReplacer("{f}", jsonEscape(file), "{d}", jsonEscape(dir)).Replace(string(raw))
	var out map[string]any
	_ = json.Unmarshal([]byte(text), &out)
	return out
}

func jsonEscape(s string) string {
	raw, _ := json.Marshal(s)
	return string(raw[1 : len(raw)-1])
}

func pathTemplates() []pathTemplate {
	command := func(covers, program string) pathTemplate {
		return pathTemplate{covers: covers, tool: "command", args: map[string]any{"command": program}}
	}
	return []pathTemplate{
		command("read", "cat '{f}'"),
		command("write", "printf x > '{f}'"),
		command("edit", "edit --path '{f}' --old-string canary --new-string CANARY"),
		command("replace_lines", "replace-lines --path '{f}' --start 1 --end 1 --content replaced"),
		command("stat", "stat '{f}'"),
		command("wc", "wc '{f}'"),
		command("delete", "rm '{f}'"),
		command("chmod", "chmod 600 '{f}'"),
		command("chown", "chown "+itoa(os.Getuid())+" '{f}'"),
		command("copy", "cp '{f}' copied.txt"),
		command("move", "mv '{f}' moved.txt"),
		command("mkdir", "mkdir '{d}/made'"),
		command("extract_archive", "tar -xf archive.tar -C '{d}'"),
		command("diff", "diff notes.txt '{f}'"),
		command("jq", "jq . '{f}'"),
		command("jq_edit", "jq-edit --path '{f}' --query '.canary = 1'"),
		command("git_status", "git -C '{d}' status"),
		command("git_diff", "git -C '{d}' diff"),
		command("git_log", "git -C '{d}' log -p"),
		command("git_commit", "git -C '{d}' commit -am outside"),
		command("git_show", "git -C '{d}' show HEAD"),
		command("git_blame", "git -C '{d}' blame target.json"),
		command("git_restore", "git -C '{d}' restore target.json"),
		command("git_ref", "git -C '{d}' rev-parse HEAD"),
		command("git_branches", "git -C '{d}' branch made"),
		command("git_compare", "git -C '{d}' rev-list HEAD"),
		command("git_checkout", "git -C '{d}' checkout -b made"),
		command("git_merge", "git -C '{d}' merge --no-edit HEAD"),
		command("git_stash", "git -C '{d}' stash"),
		command("git_stash_list", "git -C '{d}' stash list"),
		command("find", "find '{d}'"),
		command("grep", "grep -r canary '{d}'"),
		command("list_dir", "ls -la '{d}'"),
		{covers: "code_rewrite", tool: "code_rewrite", args: map[string]any{"pattern": "canary", "rewrite": "x", "lang": "json", "path": "{f}"}},
		{covers: "restore_version", tool: "restore_version", args: map[string]any{"path": "{f}"}},
		{covers: "source_history", tool: "source_history", args: map[string]any{"mode": "version", "path": "{f}"}},
		{covers: "scan_pack", tool: "scan_pack", args: map[string]any{"paths": []any{"{d}"}}},
		{covers: "view_image", tool: "view_image", args: map[string]any{"path": "{f}"}},
		{covers: "view_video", tool: "view_video", args: map[string]any{"path": "{f}"}},
	}
}

func itoa(n int) string {
	raw, _ := json.Marshal(n)
	return string(raw)
}

// unwitnessedPathTools are catalog path tools with no path to escape through.
var unwitnessedPathTools = map[string]string{
	"pack_board": "its plan argument filters the worker roster by Blueprint name and opens no file",
}

// catalogPathNames are the tools and operations the catalog says take paths.
func catalogPathNames(t *testing.T) []string {
	t.Helper()
	catalog, err := nativemanifest.Load()
	testutil.FailErr(t, "load native catalog", err)
	var names []string
	for name, capability := range catalog.MultiRootTools() {
		if capability == "path" || capability == "discovery" {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	return names
}

// escapeSpelling names the outside file and directory from inside the project.
type escapeSpelling struct {
	name      string
	file, dir string
}

// escapeSpellings builds each spelling over the real tree. A spelling this
// host's filesystem does not alias names a different, absent path; writing
// there is still an outside write.
func escapeSpellings(t *testing.T, p refusalProject) []escapeSpelling {
	t.Helper()
	base, outsideName := filepath.Dir(p.project), filepath.Base(p.outside)
	target := filepath.Join(p.outside, "target.json")
	testutil.FailErr(t, "link chain", os.Symlink("l2", filepath.Join(p.project, "l1")))
	testutil.FailErr(t, "link chain end", os.Symlink(filepath.Join("..", outsideName), filepath.Join(p.project, "l2")))
	testutil.FailErr(t, "hard link", os.Link(target, filepath.Join(p.project, "hl.json")))
	nfd := filepath.Join(base, strings.TrimSuffix(outsideName, "é")+"é")
	upper := filepath.Join(base, strings.ToUpper(outsideName))
	return []escapeSpelling{
		{name: "parent re-entry", file: filepath.Join("sub", "..", "..", outsideName, "target.json"), dir: filepath.Join("sub", "..", "..", outsideName)},
		{name: "symlink chain", file: filepath.Join("l1", "target.json"), dir: "l1"},
		{name: "hardlink", file: "hl.json"},
		{name: "case variant", file: filepath.Join(upper, "target.json"), dir: upper},
		{name: "NFD variant", file: filepath.Join(nfd, "target.json"), dir: nfd},
		{name: "absolute outside", file: target, dir: p.outside},
	}
}

// seedOutside makes the outside directory a repository with a dirty file.
func seedOutside(t *testing.T, p refusalProject) {
	t.Helper()
	target := filepath.Join(p.outside, "target.json")
	testutil.FailErr(t, "remove default outside file", os.Remove(filepath.Join(p.outside, "target.txt")))
	testutil.FailErr(t, "write outside file", os.WriteFile(target, []byte("{}\n"), 0o644))
	gittest.InitCommit(t, p.outside, "outside")
	testutil.FailErr(t, "dirty outside file", os.WriteFile(target, []byte(outsideCanary+"\n"), 0o644))
}

func TestRefusedPathsStayInsideTheRoots(t *testing.T) {
	gitengine.TestingEnableBundledBinary()
	templates := pathTemplates()
	covered := map[string]bool{}
	for _, tpl := range templates {
		covered[tpl.covers] = true
	}
	catalog := catalogPathNames(t)
	for _, name := range catalog {
		if !covered[name] && unwitnessedPathTools[name] == "" {
			t.Errorf("catalog path tool %q has no template", name)
		}
	}
	for name := range unwitnessedPathTools {
		if covered[name] {
			t.Errorf("unwitnessed path tool %q has a template", name)
		}
	}
	host := newProgramHost(t)
	cells := 0
	for _, tpl := range templates {
		t.Run(tpl.covers, func(t *testing.T) {
			t.Run("allowed absolute", func(t *testing.T) {
				t.Parallel()
				_, changed, delivered, cards := runPathCell(t, host, tpl, "absolute outside", approveCard)
				if !changed && !delivered && cards == 0 {
					t.Fatalf("%s reached nothing outside when allowed, so its refusal cells prove nothing", tpl.covers)
				}
			})
			for _, name := range []string{"parent re-entry", "symlink chain", "hardlink", "case variant", "NFD variant", "absolute outside"} {
				if name == "hardlink" && tpl.usesDir() {
					continue // A directory has no hard link.
				}
				cells++
				t.Run(name, func(t *testing.T) {
					t.Parallel()
					s, changed, delivered, _ := runPathCell(t, host, tpl, name, declineCard)
					// A hard link is inside the roots, so reading it is not an escape.
					if delivered && name != "hardlink" {
						t.Errorf("%s through %s delivered the outside file", tpl.covers, name)
					}
					if changed {
						t.Errorf("%s through %s changed the outside tree: %s", tpl.covers, name, describePath(t, s, tpl))
					}
				})
			}
		})
	}
	t.Logf("generator B: %d path tools, %d escape cells", len(templates), cells)
}

// runPathCell runs one templated call through one spelling, answering every
// card with decide, and reports what reached the outside tree.
func runPathCell(t *testing.T, host *programHost, tpl pathTemplate, spelling string, decide func(*testing.T, *commandProgramSession, wire.CheckpointEvent)) (s *commandProgramSession, changed, delivered bool, cards int) {
	t.Helper()
	project := newRefusalProject(t, "")
	seedOutside(t, project)
	var chosen escapeSpelling
	for _, candidate := range escapeSpellings(t, project) {
		if candidate.name == spelling {
			chosen = candidate
		}
	}
	config := &llm.MockConfig{Responses: []llm.MockResponseEntry{{Pattern: "run the program", FollowUpText: "Done."}}}
	s = host.withClientAt(t, project.project, llm.NewMockProvider(config), wire.CreateSessionRequest{}, "")
	s.h.SeedProgress(t, t.Context(), s.session)
	configDir, err := configdir.UserConfigDir()
	testutil.FailErr(t, "config dir", err)
	probe := worldProbe{db: s.h.DB, session: s.session, outside: project.outside,
		grantFile: filepath.Join(configDir, settingsoverlay.BasenameApprovals)}
	before := probe.read(t)
	beforeGit := gitDigest(t, project.outside)
	config.Responses[0].ToolCalls = []llm.MockToolCall{{ID: "witness-1", Name: tpl.tool, Args: tpl.bind(chosen.file, chosen.dir)}}
	promptDeciding(t, s, decide)
	after := probe.read(t)
	changed = before.outside != after.outside || beforeGit != gitDigest(t, project.outside)
	msgs, err := s.h.Store.GetMessages(t.Context(), s.session)
	testutil.FailErr(t, "read transcript", err)
	raw, err := json.Marshal(msgs)
	testutil.FailErr(t, "encode transcript", err)
	delivered = strings.Contains(string(raw), "outside 5c1d7a")
	testutil.FailErr(t, "count cards", s.h.DB.QueryRowContext(t.Context(),
		`SELECT COUNT(*) FROM checkpoints WHERE session_id = ? AND kind = 'tool_approval'`, s.session).Scan(&cards))
	return s, changed, delivered, cards
}

func describePath(t *testing.T, s *commandProgramSession, tpl pathTemplate) string {
	t.Helper()
	return describeWitness(t, s, refusalWitness{covers: tpl.covers, tool: tpl.tool})
}
