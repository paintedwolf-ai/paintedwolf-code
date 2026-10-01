package wirespec

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/lycaon/lycaon/internal/tsparse"
	"github.com/odvcencio/gotreesitter"
	"github.com/odvcencio/gotreesitter/grammars"
)

type clientInterface struct {
	methods map[string]struct{}
	bases   []string
}

type clientImport struct{ path, name string }

func ParseTSClientMethods(path string) (map[string]struct{}, error) {
	return parseTSInterfaceMethods(path, "LycaonClient", make(map[string]bool))
}

func parseTSInterfaceMethods(path, name string, visiting map[string]bool) (map[string]struct{}, error) {
	path = filepath.Clean(path)
	key := path + ":" + name
	if visiting[key] {
		return nil, fmt.Errorf("cyclic client interface inheritance: %s", key)
	}
	visiting[key] = true
	defer delete(visiting, key)
	interfaces, imports, err := readClientInterfaces(path)
	if err != nil {
		return nil, err
	}
	declaration, ok := interfaces[name]
	if !ok {
		return nil, fmt.Errorf("client interface %s not found in %s", name, path)
	}
	for _, base := range declaration.bases {
		parentPath, parentName := path, base
		if imported, ok := imports[base]; ok {
			if !strings.HasPrefix(imported.path, ".") {
				return nil, fmt.Errorf("client interface %s has nonlocal base %s", key, base)
			}
			parentPath, parentName = filepath.Join(filepath.Dir(path), imported.path), imported.name
		}
		methods, err := parseTSInterfaceMethods(parentPath, parentName, visiting)
		if err != nil {
			return nil, err
		}
		for method := range methods {
			declaration.methods[method] = struct{}{}
		}
	}
	return declaration.methods, nil
}

func readClientInterfaces(path string) (map[string]clientInterface, map[string]clientImport, error) {
	body, err := os.ReadFile(path)
	if err != nil {
		return nil, nil, err
	}
	language := grammars.DetectLanguageByName("typescript").Language()
	tree, err := tsparse.ParseWithin(context.Background(), language, body, 10*time.Second)
	if err != nil {
		return nil, nil, err
	}
	defer tree.Release()
	if tree.RootNode() == nil || tree.RootNode().HasErrorOrMissing() {
		return nil, nil, fmt.Errorf("invalid TypeScript client contract: %s", path)
	}
	interfaces := map[string]clientInterface{}
	imports := map[string]clientImport{}
	var visit func(*gotreesitter.Node)
	visit = func(node *gotreesitter.Node) {
		switch node.Type(language) {
		case "interface_declaration":
			name := clientNodeText(node.ChildByFieldName("name", language), body)
			declaration := clientInterface{methods: map[string]struct{}{}}
			if members := node.ChildByFieldName("body", language); members != nil {
				for i := 0; i < members.NamedChildCount(); i++ {
					member := members.NamedChild(i)
					if member.Type(language) == "method_signature" {
						declaration.methods[clientNodeText(member.ChildByFieldName("name", language), body)] = struct{}{}
					}
				}
			}
			for i := 0; i < node.NamedChildCount(); i++ {
				child := node.NamedChild(i)
				if child.Type(language) == "extends_type_clause" {
					for j := 0; j < child.NamedChildCount(); j++ {
						declaration.bases = append(declaration.bases, clientNodeText(child.NamedChild(j), body))
					}
				}
			}
			interfaces[name] = declaration
			return
		case "import_statement":
			source := strings.Trim(clientNodeText(node.ChildByFieldName("source", language), body), "\"'")
			var collect func(*gotreesitter.Node)
			collect = func(part *gotreesitter.Node) {
				if part.Type(language) == "import_specifier" {
					name := clientNodeText(part.ChildByFieldName("name", language), body)
					alias := clientNodeText(part.ChildByFieldName("alias", language), body)
					if alias == "" {
						alias = name
					}
					imports[alias] = clientImport{source, name}
					return
				}
				for i := 0; i < part.NamedChildCount(); i++ {
					collect(part.NamedChild(i))
				}
			}
			collect(node)
			return
		}
		for i := 0; i < node.NamedChildCount(); i++ {
			visit(node.NamedChild(i))
		}
	}
	visit(tree.RootNode())
	return interfaces, imports, nil
}

func clientNodeText(node *gotreesitter.Node, body []byte) string {
	if node == nil {
		return ""
	}
	return string(body[node.StartByte():node.EndByte()])
}
