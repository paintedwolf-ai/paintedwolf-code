package contract

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"

	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

type projectSpaceDenScan struct {
	Violations []string
}

func scanProjectSpaceDen(root string) (*projectSpaceDenScan, error) {
	out := &projectSpaceDenScan{}
	denRoot := filepath.Join(root, "lycaon-den")
	for _, rel := range []string{
		"src/components/NewSessionModal.tsx",
		"src/components/nav/ProjectsNavMenu.tsx",
		"src/components/nav/ProjectsSidebar.tsx",
	} {
		if _, err := os.Stat(filepath.Join(denRoot, rel)); err == nil {
			out.Violations = append(out.Violations, "forbidden file still exists: lycaon-den/"+rel)
		}
	}
	srcDirs := []string{
		filepath.Join(denRoot, "src"),
		filepath.Join(denRoot, "shared"),
	}
	banned := []string{
		"NewSessionModal",
		"ProjectsNavMenu",
		"new-session-modal",
		"DenProject",
	}
	for _, dir := range srcDirs {
		if err := filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() || (!strings.HasSuffix(path, ".ts") && !strings.HasSuffix(path, ".tsx")) {
				return nil
			}
			if strings.HasSuffix(path, ".test.ts") || strings.HasSuffix(path, ".test.tsx") {
				return nil
			}
			data, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			text := string(data)
			rel, _ := filepath.Rel(root, path)
			for _, needle := range banned {
				if strings.Contains(text, needle) {
					out.Violations = append(out.Violations, rel+" contains banned symbol "+needle)
				}
			}
			return nil
		}); err != nil {
			return nil, err
		}
	}
	settingsPath := filepath.Join(denRoot, "src", "settings", "settings-nav-model.ts")
	data, err := os.ReadFile(settingsPath)
	if err != nil {
		return nil, err
	}
	text := string(data)
	appStart := strings.Index(text, "export const APP_SETTINGS")
	projectCtxIdx := strings.Index(text, "export const PROJECT_CONTEXT")
	appBlock := ""
	if appStart >= 0 && projectCtxIdx > appStart {
		appBlock = text[appStart:projectCtxIdx]
	} else if appStart >= 0 {
		appBlock = text[appStart:]
	}
	for _, moved := range []string{`id: "models"`, `id: "edit-review"`, `id: "budgets"`, `id: "tests"`} {
		if strings.Contains(appBlock, moved) {
			out.Violations = append(out.Violations, "settings-nav-model.ts lists moved section in APP_SETTINGS: "+moved)
		}
	}
	return out, nil
}

type agentsMDWriteScan struct {
	Violations []string
}

func scanHardcodedAgentsMDWrites(lycaonRoot string) (*agentsMDWriteScan, error) {
	out := &agentsMDWriteScan{}
	fset := token.NewFileSet()
	err := filepath.WalkDir(filepath.Join(lycaonRoot, "internal"), func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		f, err := parser.ParseFile(fset, path, nil, 0)
		if err != nil {
			return err
		}
		ast.Inspect(f, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok || sel.Sel.Name != "WriteFile" {
				return true
			}
			if len(call.Args) == 0 {
				return true
			}
			pathLit := contractcheck.AstStringLit(call.Args[0])
			if pathLit == "" {
				return true
			}
			if strings.Contains(pathLit, "AGENTS.md") {
				rel, _ := filepath.Rel(lycaonRoot, path)
				out.Violations = append(out.Violations, rel+": hardcoded AGENTS.md write bypasses the reviewed file-change path")
			}
			return true
		})
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}
