package sourceview

import (
	"bytes"
	"context"
	"html"

	"github.com/odvcencio/gotreesitter"
	"github.com/odvcencio/gotreesitter/grammars"
)

type vueScript struct {
	name       string
	attributes map[string]string
	body       *gotreesitter.Node
}

func vueScripts(ctx context.Context, source []byte) ([]ScriptProjection, []Limitation, error) {
	grammar := grammars.VueLanguage()
	tree, limitations, err := parseVueComponent(ctx, source, grammar)
	if err != nil {
		return nil, nil, err
	}
	defer tree.Release()
	root := tree.RootNode()
	var blocks []vueScript
	originalLines := lineOffsets(source)
	for i := 0; i < root.NamedChildCount(); i++ {
		node := root.NamedChild(i)
		if node.Type(grammar) != "script_element" {
			continue
		}
		block, err := readVueScript(node, grammar, source)
		if err != nil {
			return nil, nil, err
		}
		if block.name != "script" {
			continue
		}
		if _, external := block.attributes["src"]; !external && (block.body == nil || len(bytes.TrimSpace(source[block.body.StartByte():block.body.EndByte()])) == 0) {
			continue
		}
		blocks = append(blocks, block)
	}
	if err := validateVueScripts(blocks); err != nil {
		return nil, nil, err
	}
	var projections []ScriptProjection
	for _, block := range blocks {
		info, err := vueScriptInfo(block.attributes)
		if err != nil {
			return nil, nil, err
		}
		if info.mode == dataScript || block.body == nil {
			continue
		}
		builder := scriptBuilder{originalLines: originalLines, original: source, extension: info.extension, fragments: []scriptFragment{{start: int(block.body.StartByte()), end: int(block.body.EndByte())}}}
		projections = append(projections, builder.build())
	}
	return projections, limitations, nil
}

func readVueScript(node *gotreesitter.Node, grammar *gotreesitter.Language, source []byte) (vueScript, error) {
	block := vueScript{attributes: make(map[string]string)}
	for i := 0; i < node.NamedChildCount(); i++ {
		child := node.NamedChild(i)
		switch child.Type(grammar) {
		case "raw_text":
			block.body = child
		case "start_tag":
			for j := 0; j < child.NamedChildCount(); j++ {
				attribute := child.NamedChild(j)
				if attribute.Type(grammar) == "tag_name" {
					block.name = string(source[attribute.StartByte():attribute.EndByte()])
				}
				if attribute.Type(grammar) != "attribute" {
					continue
				}
				key, value := vueAttribute(attribute, grammar, source)
				if _, exists := block.attributes[key]; exists {
					return block, &Limitation{Construct: "vue_sfc", Detail: "Vue script has a duplicate attribute"}
				}
				block.attributes[key] = value
			}
		}
	}
	return block, nil
}

func vueAttribute(node *gotreesitter.Node, grammar *gotreesitter.Language, source []byte) (string, string) {
	var key, value string
	for i := 0; i < node.NamedChildCount(); i++ {
		child := node.NamedChild(i)
		raw := source[child.StartByte():child.EndByte()]
		switch child.Type(grammar) {
		case "attribute_name":
			key = string(raw)
		case "attribute_value":
			value = html.UnescapeString(string(raw))
		case "quoted_attribute_value":
			value = html.UnescapeString(string(raw[1 : len(raw)-1]))
		}
	}
	return key, value
}

func validateVueScripts(blocks []vueScript) error {
	var normal, setup *vueScript
	for i := range blocks {
		block := &blocks[i]
		destination := &normal
		if _, exists := block.attributes["setup"]; exists {
			destination = &setup
		}
		if *destination != nil {
			return &Limitation{Construct: "vue_sfc", Detail: "Vue component has duplicate script blocks"}
		}
		*destination = block
	}
	if normal != nil && setup != nil {
		if normal.attributes["lang"] != setup.attributes["lang"] {
			return &Limitation{Construct: "vue_sfc", Detail: "Vue script and script setup use different languages"}
		}
		if normal.attributes["src"] != "" {
			return &Limitation{Construct: "vue_sfc", Detail: "Vue external script cannot be combined with script setup"}
		}
	}
	return nil
}
