// Package workercompletionxml holds the parent <task> XML document.
package workercompletionxml

import (
	"encoding/xml"
	"strings"
)

// Doc is the XML document marshaled into parent tool-result content. The
// coordinator reads it, so it names the worker; the job and child session are
// host identity carried by the message's worker summary.
type Doc struct {
	XMLName             xml.Name `xml:"task"`
	Worker              string   `xml:"worker,attr,omitempty"`
	AgentType           string   `xml:"agent_type,attr,omitempty"`
	State               string   `xml:"state,attr"`
	MergeStatus         string   `xml:"merge_status,attr,omitempty"`
	HintCode            string   `xml:"hint_code,attr,omitempty"`
	Summary             string   `xml:"summary"`
	TaskResult          string   `xml:"task_result,omitempty"`
	Digest              string   `xml:"digest,omitempty"`
	ProofJSON           string   `xml:"proof_json,omitempty"`
	ReportJSON          string   `xml:"report_json,omitempty"`
	DecisionRequestJSON string   `xml:"decision_request_json,omitempty"`
}

// Unmarshal parses a <task> completion block. ok is false when content is empty
// or not task XML.
func Unmarshal(content string) (Doc, bool) {
	content = strings.TrimSpace(content)
	if content == "" || !strings.Contains(content, "<task") {
		return Doc{}, false
	}
	var doc Doc
	if err := xml.Unmarshal([]byte(content), &doc); err != nil {
		return Doc{}, false
	}
	return doc, true
}
