package sourceview

import (
	"strings"

	"golang.org/x/net/html"
)

type scriptMode uint8

const (
	dataScript scriptMode = iota
	classicScript
	moduleScript
)

type scriptInfo struct {
	mode      scriptMode
	extension string
}

func classifyHTMLScript(attrs []html.Attribute) scriptInfo {
	values := make(map[string]string)
	for _, attr := range attrs {
		if _, exists := values[attr.Key]; !exists {
			values[attr.Key] = attr.Val
		}
	}
	if _, external := values["src"]; external {
		return scriptInfo{}
	}
	mediaType, explicit := values["type"]
	if !explicit && values["language"] != "" {
		mediaType = "text/" + values["language"]
	} else if mediaType == "" {
		mediaType = "text/javascript"
	} else {
		mediaType = strings.Trim(mediaType, "\t\n\f\r ")
	}
	return scriptInfo{mode: scriptType(asciiLower(mediaType)), extension: "js"}
}

func vueScriptInfo(values map[string]string) (scriptInfo, error) {
	if values["src"] != "" {
		if _, setup := values["setup"]; setup {
			return scriptInfo{}, &Limitation{Construct: "vue_sfc", Detail: "Vue script setup cannot use an external source"}
		}
		return scriptInfo{}, nil
	}
	info := scriptInfo{mode: classicScript, extension: "js"}
	lang := values["lang"]
	switch lang {
	case "", "js":
		return info, nil
	case "ts", "jsx", "tsx":
		info.extension = lang
		return info, nil
	default:
		return scriptInfo{}, &Limitation{Construct: "embedded_language", Detail: "Embedded script language " + lang + " has no bundled security analysis"}
	}
}

func asciiLower(value string) string {
	lower := []byte(value)
	for i, b := range lower {
		if b >= 'A' && b <= 'Z' {
			lower[i] = b + ('a' - 'A')
		}
	}
	return string(lower)
}

// HTML uses an exact MIME essence string, not a parsed Content-Type header.
func scriptType(value string) scriptMode {
	switch value {
	case "module":
		return moduleScript
	case "application/ecmascript", "application/javascript", "application/x-ecmascript", "application/x-javascript",
		"text/ecmascript", "text/javascript", "text/javascript1.0", "text/javascript1.1", "text/javascript1.2",
		"text/javascript1.3", "text/javascript1.4", "text/javascript1.5", "text/jscript", "text/livescript",
		"text/x-ecmascript", "text/x-javascript":
		return classicScript
	default:
		return dataScript
	}
}
