package xrefs

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	"github.com/lycaon/lycaon/internal/archlint"
)

// Finding kinds.
const (
	KindUninvokedCommand   = "uninvoked-tauri-command"
	KindUnpermittedCommand = "unpermitted-tauri-command"
	KindUnregisteredInvoke = "unregistered-tauri-invoke"
)

// TauriLayout names where the desktop shell declares and Den calls commands.
type TauriLayout struct {
	Handler      string // Rust file holding generate_handler!
	Permissions  string // directory of permission TOML files
	Capabilities string // directory of capability JSON files
	Source       string // Den source directory
}

// DenTauri is the repository's layout.
var DenTauri = TauriLayout{
	Handler:      "lycaon-den/src-tauri/src/lib.rs",
	Permissions:  "lycaon-den/src-tauri/permissions",
	Capabilities: "lycaon-den/src-tauri/capabilities",
	Source:       "lycaon-den/src",
}

var (
	handlerBlock   = regexp.MustCompile(`(?s)generate_handler!\[(.*?)\]`)
	rustComment    = regexp.MustCompile(`(?m)//.*$`)
	rustAttribute  = regexp.MustCompile(`#\[[^\]]*\]`)
	commandRename  = regexp.MustCompile(`(?s)#\[tauri::command\(\s*rename\s*=\s*"([^"]+)"[^\]]*\)\]\s*(?:#\[[^\]]*\]\s*)*pub(?:\([a-z]+\))?\s+(?:async\s+)?fn\s+([A-Za-z0-9_]+)`)
	invokeLiteral  = regexp.MustCompile(`\binvoke(?:<[^>()]*>)?\(\s*["']([A-Za-z0-9_]+)["']`)
	stringLiteral  = regexp.MustCompile(`["']([a-z][a-z0-9_]*)["']`)
	permissionID   = regexp.MustCompile(`(?m)^\s*identifier\s*=\s*"([^"]+)"`)
	permissionCmds = regexp.MustCompile(`(?m)^\s*commands\.allow\s*=\s*\[([^\]]*)\]`)
	quoted         = regexp.MustCompile(`"([^"]+)"`)
	denTest        = regexp.MustCompile(`(\.test\.|\.spec\.|/test/|/__tests__/|/mocks/)`)
)

// Tauri reports registered commands no Den production code names, commands no
// capability permits, and Den invokes of commands nothing registers.
func Tauri(root string, layout TauriLayout) ([]archlint.Finding, error) {
	handler, err := os.ReadFile(filepath.Join(root, layout.Handler))
	if err != nil {
		return nil, err
	}
	renames, err := commandRenames(filepath.Dir(filepath.Join(root, layout.Handler)))
	if err != nil {
		return nil, err
	}
	var commands []string
	for _, fn := range registeredCommands(string(handler)) {
		if name, ok := renames[fn]; ok {
			fn = name
		}
		commands = append(commands, fn)
	}

	named := map[string]bool{}
	type invoke struct{ name, pos string }
	var invokes []invoke
	err = filepath.WalkDir(filepath.Join(root, layout.Source), func(p string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		rel, _ := filepath.Rel(root, p)
		rel = filepath.ToSlash(rel)
		if ext := filepath.Ext(p); (ext != ".ts" && ext != ".tsx") || denTest.MatchString(rel) || strings.HasSuffix(rel, ".d.ts") {
			return nil
		}
		raw, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		for _, m := range stringLiteral.FindAllStringSubmatch(string(raw), -1) {
			named[m[1]] = true
		}
		for _, m := range invokeLiteral.FindAllStringSubmatch(string(raw), -1) {
			invokes = append(invokes, invoke{m[1], rel})
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	permitted, err := permittedCommands(root, layout)
	if err != nil {
		return nil, err
	}

	var out []archlint.Finding
	for _, c := range commands {
		if !named[c] {
			out = append(out, archlint.Finding{
				Kind: KindUninvokedCommand, Key: c, Pos: layout.Handler,
				Message: "Tauri command " + c + " is registered but no Den production code invokes it",
			})
		}
		if !permitted[c] {
			out = append(out, archlint.Finding{
				Kind: KindUnpermittedCommand, Key: c, Pos: layout.Handler,
				Message: "Tauri command " + c + " is registered but no capability permits it",
			})
		}
	}
	reported := map[string]bool{}
	for _, inv := range invokes {
		if !slices.Contains(commands, inv.name) && !reported[inv.name] {
			reported[inv.name] = true
			out = append(out, archlint.Finding{
				Kind: KindUnregisteredInvoke, Key: inv.name, Pos: inv.pos,
				Message: "Den invokes " + inv.name + " but no Tauri command registers it",
			})
		}
	}
	return out, nil
}

func registeredCommands(src string) []string {
	m := handlerBlock.FindStringSubmatch(rustAttribute.ReplaceAllString(rustComment.ReplaceAllString(src, ""), ""))
	if m == nil {
		return nil
	}
	body := m[1]
	var out []string
	for _, entry := range strings.Split(body, ",") {
		entry = strings.TrimSpace(entry)
		if entry == "" {
			continue
		}
		if i := strings.LastIndex(entry, "::"); i >= 0 {
			entry = entry[i+2:]
		}
		out = append(out, entry)
	}
	return out
}

// commandRenames maps Rust functions to the names their command attribute
// gives them.
func commandRenames(dir string) (map[string]string, error) {
	out := map[string]string{}
	err := filepath.WalkDir(dir, func(p string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() || filepath.Ext(p) != ".rs" {
			return err
		}
		raw, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		for _, m := range commandRename.FindAllStringSubmatch(string(raw), -1) {
			out[m[2]] = m[1]
		}
		return nil
	})
	return out, err
}

// permittedCommands are the commands some permission allows when a capability
// grants that permission.
func permittedCommands(root string, layout TauriLayout) (map[string]bool, error) {
	granted := map[string]bool{}
	caps, err := filepath.Glob(filepath.Join(root, layout.Capabilities, "*.json"))
	if err != nil {
		return nil, err
	}
	for _, p := range caps {
		raw, err := os.ReadFile(p)
		if err != nil {
			return nil, err
		}
		var capability struct {
			Permissions []json.RawMessage `json:"permissions"`
		}
		if err := json.Unmarshal(raw, &capability); err != nil {
			return nil, err
		}
		for _, entry := range capability.Permissions {
			var id string
			if json.Unmarshal(entry, &id) != nil {
				var obj struct {
					Identifier string `json:"identifier"`
				}
				_ = json.Unmarshal(entry, &obj)
				id = obj.Identifier
			}
			granted[id] = true
		}
	}
	permitted := map[string]bool{}
	perms, err := filepath.Glob(filepath.Join(root, layout.Permissions, "*.toml"))
	if err != nil {
		return nil, err
	}
	for _, p := range perms {
		raw, err := os.ReadFile(p)
		if err != nil {
			return nil, err
		}
		for _, block := range strings.Split(string(raw), "[[permission]]") {
			id := permissionID.FindStringSubmatch(block)
			cmds := permissionCmds.FindStringSubmatch(block)
			if id == nil || cmds == nil || !granted[id[1]] {
				continue
			}
			for _, q := range quoted.FindAllStringSubmatch(cmds[1], -1) {
				permitted[q[1]] = true
			}
		}
	}
	return permitted, nil
}
