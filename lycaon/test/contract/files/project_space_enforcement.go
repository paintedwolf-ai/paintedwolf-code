package contract

import (
	"os"
	"path/filepath"
	"strings"
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
