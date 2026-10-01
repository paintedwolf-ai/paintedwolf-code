package definition

import (
	"fmt"
	"reflect"
	"strings"

	"gopkg.in/yaml.v3"
)

// ManifestField is one YAML-visible manifest field for docs projection. Path
// flattens list and map levels; Section is the containing block's path, so two
// blocks of one type in different places stay separate sections.
type ManifestField struct {
	Section string
	Key     string
	Path    string
	Type    string
}

// manifestRootSection is the section name for top-level manifest keys.
const manifestRootSection = "workflow"

// ManifestFieldInventory returns one row per yaml-tagged field reachable from
// the manifest root struct, in declaration order.
func ManifestFieldInventory() []ManifestField {
	var out []ManifestField
	walkManifestType(reflect.TypeOf(workflowFile{}), "", manifestRootSection, map[reflect.Type]bool{}, &out)
	return out
}

// yamlNodeType is rendered as an opaque leaf: a yaml.Node field is raw YAML the
// loader interprets itself, and its Go shape is not manifest vocabulary.
var yamlNodeType = reflect.TypeOf(yaml.Node{})

func walkManifestType(t reflect.Type, prefix, section string, visiting map[reflect.Type]bool, out *[]ManifestField) {
	t = derefManifestType(t)
	if t.Kind() != reflect.Struct || t == yamlNodeType {
		return
	}
	if visiting[t] {
		return
	}
	visiting[t] = true
	defer delete(visiting, t)

	for i := 0; i < t.NumField(); i++ {
		field := t.Field(i)
		key := yamlFieldKey(field)
		if key == "" {
			continue
		}
		path := key
		if prefix != "" {
			path = prefix + "." + key
		}
		*out = append(*out, ManifestField{
			Section: section,
			Key:     key,
			Path:    path,
			Type:    manifestFieldType(field.Type),
		})
		if nested := manifestNestedStruct(field.Type); nested != nil {
			walkManifestType(nested, path, path, visiting, out)
		}
	}
}

// yamlFieldKey returns the field's YAML key.
func yamlFieldKey(field reflect.StructField) string {
	tag, ok := field.Tag.Lookup("yaml")
	if !ok {
		return ""
	}
	key := strings.TrimSpace(strings.Split(tag, ",")[0])
	if key == "" || key == "-" {
		return ""
	}
	return key
}

// manifestNestedStruct returns the struct type a field recurses into, or nil
// when the field is a leaf.
func manifestNestedStruct(t reflect.Type) reflect.Type {
	t = derefManifestType(t)
	switch t.Kind() {
	case reflect.Slice, reflect.Array, reflect.Map:
		return manifestNestedStruct(t.Elem())
	case reflect.Struct:
		if t == yamlNodeType {
			return nil
		}
		return t
	default:
		return nil
	}
}

// manifestFieldType renders a Go type the way the docs table shows it.
func manifestFieldType(t reflect.Type) string {
	t = derefManifestType(t)
	if t == yamlNodeType {
		return "yaml"
	}
	switch t.Kind() {
	case reflect.String:
		return "string"
	case reflect.Bool:
		return "bool"
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		return "int"
	case reflect.Float32, reflect.Float64:
		return "float"
	case reflect.Interface:
		return "any"
	case reflect.Struct:
		return "block"
	case reflect.Slice, reflect.Array:
		return "[]" + manifestFieldType(t.Elem())
	case reflect.Map:
		return fmt.Sprintf("map[%s]%s", manifestFieldType(t.Key()), manifestFieldType(t.Elem()))
	default:
		return t.Kind().String()
	}
}

func derefManifestType(t reflect.Type) reflect.Type {
	for t != nil && t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	return t
}
